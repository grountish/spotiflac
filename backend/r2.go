package backend

import (
	"context"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// defaultR2QuotaBytes matches the Cloudflare R2 free tier (10 GB of stored
// data). Cloudflare bills storage in decimal GB, so 10 GB is 10e9 bytes.
const defaultR2QuotaBytes int64 = 10 * 1000 * 1000 * 1000

// r2UsageCacheTTL bounds how often a push re-walks the bucket. Listing is a
// Class B operation per 1000 objects, so this avoids a full sweep per upload.
const r2UsageCacheTTL = 5 * time.Minute

type r2Config struct {
	accountID string
	accessKey string
	secretKey string
	bucket    string
	quota     int64
	enforce   bool
}

// R2Usage reports how much of the configured quota the bucket is using.
type R2Usage struct {
	Bucket     string  `json:"bucket"`
	Bytes      int64   `json:"bytes"`
	Objects    int64   `json:"objects"`
	LimitBytes int64   `json:"limitBytes"`
	FreeBytes  int64   `json:"freeBytes"`
	Percent    float64 `json:"percent"`
	Enforced   bool    `json:"enforced"`
	CheckedAt  string  `json:"checkedAt"`
	Cached     bool    `json:"cached"`
}

type r2UsageCacheEntry struct {
	usage   R2Usage
	fetched time.Time
}

var (
	r2UsageMu    sync.Mutex
	r2UsageCache = map[string]r2UsageCacheEntry{}
)

func loadR2Config() (r2Config, error) {
	settings, err := LoadConfigSettings()
	if err != nil {
		return r2Config{}, err
	}
	if settings == nil {
		return r2Config{}, fmt.Errorf("R2 is not configured")
	}

	get := func(k string) string {
		v, _ := settings[k].(string)
		return strings.TrimSpace(v)
	}
	cfg := r2Config{
		accountID: get("r2AccountId"),
		accessKey: get("r2AccessKeyId"),
		secretKey: get("r2SecretAccessKey"),
		bucket:    get("r2Bucket"),
		quota:     r2QuotaBytes(settings),
		enforce:   r2EnforceQuota(settings),
	}

	var missing []string
	if cfg.accountID == "" {
		missing = append(missing, "Account ID")
	}
	if cfg.accessKey == "" {
		missing = append(missing, "Access Key ID")
	}
	if cfg.secretKey == "" {
		missing = append(missing, "Secret Access Key")
	}
	if cfg.bucket == "" {
		missing = append(missing, "Bucket")
	}
	if len(missing) > 0 {
		return r2Config{}, fmt.Errorf("R2 not configured: missing %s", strings.Join(missing, ", "))
	}
	return cfg, nil
}

func newR2Client(cfg r2Config) (*minio.Client, error) {
	endpoint := fmt.Sprintf("%s.r2.cloudflarestorage.com", cfg.accountID)
	return minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.accessKey, cfg.secretKey, ""),
		Secure: true,
		Region: "auto",
	})
}

// TestR2Connection verifies the configured credentials by checking the bucket.
func TestR2Connection() error {
	cfg, err := loadR2Config()
	if err != nil {
		return err
	}
	client, err := newR2Client(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	exists, err := client.BucketExists(ctx, cfg.bucket)
	if err != nil {
		return fmt.Errorf("R2 connection failed: %v", err)
	}
	if !exists {
		return fmt.Errorf("bucket %q not found or not accessible", cfg.bucket)
	}
	return nil
}

// r2QuotaBytes reads the configured storage ceiling in GB, falling back to the
// free-tier allowance. A value of 0 or less disables the ceiling.
func r2QuotaBytes(settings map[string]interface{}) int64 {
	raw, ok := settings["r2QuotaGB"]
	if !ok {
		return defaultR2QuotaBytes
	}
	gb, ok := raw.(float64)
	if !ok || gb <= 0 {
		return 0
	}
	return int64(gb * 1000 * 1000 * 1000)
}

func r2EnforceQuota(settings map[string]interface{}) bool {
	raw, ok := settings["r2EnforceQuota"]
	if !ok {
		return true
	}
	enforce, ok := raw.(bool)
	if !ok {
		return true
	}
	return enforce
}

func humanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}

// GetR2Usage reports total stored bytes and object count for the configured
// bucket. Cloudflare exposes no size field over the S3 API, so this sums the
// object listing. Results are cached for r2UsageCacheTTL unless refresh is set.
func GetR2Usage(refresh bool) (R2Usage, error) {
	cfg, err := loadR2Config()
	if err != nil {
		return R2Usage{}, err
	}
	return getR2UsageFor(cfg, refresh)
}

func getR2UsageFor(cfg r2Config, refresh bool) (R2Usage, error) {
	cacheKey := cfg.accountID + "/" + cfg.bucket

	if !refresh {
		r2UsageMu.Lock()
		entry, ok := r2UsageCache[cacheKey]
		r2UsageMu.Unlock()
		if ok && time.Since(entry.fetched) < r2UsageCacheTTL {
			usage := entry.usage
			usage.Cached = true
			usage.LimitBytes = cfg.quota
			usage.Enforced = cfg.enforce && cfg.quota > 0
			usage.FreeBytes, usage.Percent = r2QuotaMath(usage.Bytes, cfg.quota)
			return usage, nil
		}
	}

	client, err := newR2Client(cfg)
	if err != nil {
		return R2Usage{}, err
	}

	// A full bucket walk can be slow on large buckets; cap it rather than
	// letting a push block indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var total, count int64
	for object := range client.ListObjects(ctx, cfg.bucket, minio.ListObjectsOptions{Recursive: true}) {
		if object.Err != nil {
			return R2Usage{}, fmt.Errorf("failed to list %s: %v", cfg.bucket, object.Err)
		}
		total += object.Size
		count++
	}

	usage := R2Usage{
		Bucket:    cfg.bucket,
		Bytes:     total,
		Objects:   count,
		CheckedAt: time.Now().Format(time.RFC3339),
	}
	usage.LimitBytes = cfg.quota
	usage.Enforced = cfg.enforce && cfg.quota > 0
	usage.FreeBytes, usage.Percent = r2QuotaMath(total, cfg.quota)

	r2UsageMu.Lock()
	r2UsageCache[cacheKey] = r2UsageCacheEntry{usage: usage, fetched: time.Now()}
	r2UsageMu.Unlock()

	return usage, nil
}

func r2QuotaMath(used, limit int64) (free int64, percent float64) {
	if limit <= 0 {
		return 0, 0
	}
	free = limit - used
	if free < 0 {
		free = 0
	}
	return free, float64(used) / float64(limit) * 100
}

// addUsedBytes keeps the cached total in step with what a push just uploaded,
// so back-to-back pushes see an accurate figure without re-listing the bucket.
func addUsedBytes(cfg r2Config, delta int64) {
	if delta == 0 {
		return
	}
	cacheKey := cfg.accountID + "/" + cfg.bucket
	r2UsageMu.Lock()
	defer r2UsageMu.Unlock()
	entry, ok := r2UsageCache[cacheKey]
	if !ok {
		return
	}
	entry.usage.Bytes += delta
	entry.usage.FreeBytes, entry.usage.Percent = r2QuotaMath(entry.usage.Bytes, entry.usage.LimitBytes)
	r2UsageCache[cacheKey] = entry
}

// collectUploadFiles expands the requested paths into concrete (localPath, key)
// uploads plus their total size, mirroring the layout PushToR2 writes.
func collectUploadFiles(paths []string) (files []r2UploadItem, totalBytes int64) {
	for _, p := range paths {
		info, statErr := os.Stat(p)
		if statErr != nil {
			continue
		}
		if info.IsDir() {
			base := filepath.Base(strings.TrimRight(p, string(os.PathSeparator)))
			filepath.Walk(p, func(fp string, fi os.FileInfo, walkErr error) error {
				if walkErr != nil || fi.IsDir() {
					return nil
				}
				rel, relErr := filepath.Rel(p, fp)
				if relErr != nil {
					rel = filepath.Base(fp)
				}
				files = append(files, r2UploadItem{
					localPath: fp,
					key:       filepath.ToSlash(filepath.Join(base, rel)),
					size:      fi.Size(),
				})
				totalBytes += fi.Size()
				return nil
			})
			continue
		}
		files = append(files, r2UploadItem{localPath: p, key: filepath.Base(p), size: info.Size()})
		totalBytes += info.Size()
	}
	return files, totalBytes
}

type r2UploadItem struct {
	localPath string
	key       string
	size      int64
}

// PushToR2 uploads the given files/folders to the configured R2 bucket. A folder
// is uploaded recursively under a top-level key prefix named after the folder;
// loose files are uploaded under their basename.
func PushToR2(paths []string) (string, error) {
	if len(paths) == 0 {
		return "", fmt.Errorf("no paths to upload")
	}
	cfg, err := loadR2Config()
	if err != nil {
		return "", err
	}
	client, err := newR2Client(cfg)
	if err != nil {
		return "", err
	}

	files, incoming := collectUploadFiles(paths)
	if len(files) == 0 {
		return "", fmt.Errorf("nothing uploaded")
	}

	// Refuse the whole push up front when it cannot fit, so a large album does
	// not upload half of itself before hitting the ceiling.
	remaining := int64(-1)
	if cfg.enforce && cfg.quota > 0 {
		usage, usageErr := getR2UsageFor(cfg, false)
		if usageErr != nil {
			return "", fmt.Errorf("could not check R2 usage before upload: %v", usageErr)
		}
		remaining = cfg.quota - usage.Bytes
		if incoming > remaining {
			return "", fmt.Errorf(
				"R2 storage limit reached: %s of %s used, this upload needs %s but only %s is free",
				humanBytes(usage.Bytes), humanBytes(cfg.quota),
				humanBytes(incoming), humanBytes(maxInt64(remaining, 0)),
			)
		}
	}

	ctx := context.Background()
	uploaded := 0
	var uploadedBytes int64
	var firstErr error

	for _, item := range files {
		// Guard again per object: the pre-flight figure can be stale if another
		// client is writing to the same bucket.
		if remaining >= 0 && uploadedBytes+item.size > remaining {
			if firstErr == nil {
				firstErr = fmt.Errorf("stopped at %s: R2 storage limit of %s reached", item.key, humanBytes(cfg.quota))
			}
			break
		}
		contentType := mime.TypeByExtension(filepath.Ext(item.localPath))
		if _, e := client.FPutObject(ctx, cfg.bucket, item.key, item.localPath, minio.PutObjectOptions{ContentType: contentType}); e != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %v", item.key, e)
			}
			continue
		}
		uploaded++
		uploadedBytes += item.size
	}

	addUsedBytes(cfg, uploadedBytes)

	if uploaded == 0 {
		if firstErr != nil {
			return "", firstErr
		}
		return "", fmt.Errorf("nothing uploaded")
	}
	if firstErr != nil {
		return fmt.Sprintf("uploaded %d file(s) to %s (some failed)", uploaded, cfg.bucket), firstErr
	}
	return fmt.Sprintf("uploaded %d file(s) to %s", uploaded, cfg.bucket), nil
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
