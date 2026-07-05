package backend

import (
	"context"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type r2Config struct {
	accountID string
	accessKey string
	secretKey string
	bucket    string
}

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

	ctx := context.Background()
	uploaded := 0
	var firstErr error

	upload := func(localPath, key string) {
		contentType := mime.TypeByExtension(filepath.Ext(localPath))
		if _, e := client.FPutObject(ctx, cfg.bucket, key, localPath, minio.PutObjectOptions{ContentType: contentType}); e != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %v", key, e)
			}
			return
		}
		uploaded++
	}

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
				key := filepath.ToSlash(filepath.Join(base, rel))
				upload(fp, key)
				return nil
			})
		} else {
			upload(p, filepath.Base(p))
		}
	}

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
