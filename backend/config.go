package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

func GetDefaultMusicPath() string {

	homeDir, err := os.UserHomeDir()
	if err != nil {

		return "C:\\Users\\Public\\Music"
	}

	return filepath.Join(homeDir, "Music")
}

func GetConfigPath() (string, error) {
	dir, err := EnsureAppDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, "config.json"), nil
}

func LoadConfigSettings() (map[string]interface{}, error) {
	configPath, err := GetConfigPath()
	if err != nil {
		return nil, err
	}

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil, nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, err
	}

	return settings, nil
}

func GetRedownloadWithSuffixSetting() bool {
	settings, err := LoadConfigSettings()
	if err != nil || settings == nil {
		return false
	}

	enabled, _ := settings["redownloadWithSuffix"].(bool)
	return enabled
}

func GetAllowLossyFallbackSetting() bool {
	settings, err := LoadConfigSettings()
	if err != nil || settings == nil {
		return false
	}

	enabled, _ := settings["allowLossyFallback"].(bool)
	return enabled
}

func GetAllowFallbackSetting() bool {
	settings, err := LoadConfigSettings()
	if err != nil || settings == nil {
		return true
	}

	enabled, ok := settings["allowFallback"].(bool)
	if !ok {
		return true
	}

	return enabled
}

func GetTidalQualitySetting() string {
	settings, err := LoadConfigSettings()
	if err != nil || settings == nil {
		return "LOSSLESS"
	}

	quality, _ := settings["tidalQuality"].(string)
	switch strings.TrimSpace(strings.ToUpper(quality)) {
	case "HI_RES_LOSSLESS":
		return "HI_RES_LOSSLESS"
	case "LOSSLESS":
		return "LOSSLESS"
	default:
		return "LOSSLESS"
	}
}

func GetQobuzQualitySetting() string {
	settings, err := LoadConfigSettings()
	if err != nil || settings == nil {
		return "6"
	}

	quality, _ := settings["qobuzQuality"].(string)
	switch strings.TrimSpace(quality) {
	case "27":
		return "27"
	case "7":
		return "7"
	case "6":
		return "6"
	default:
		return "6"
	}
}

func GetLinkResolverSetting() string {
	settings, err := LoadConfigSettings()
	if err != nil || settings == nil {
		return linkResolverProviderDeezerSongLink
	}

	resolver, _ := settings["linkResolver"].(string)
	switch strings.TrimSpace(strings.ToLower(resolver)) {
	case "songlink", linkResolverProviderDeezerSongLink:
		return linkResolverProviderDeezerSongLink
	case "songstats":
		return linkResolverProviderSongstats
	case "":
		return linkResolverProviderDeezerSongLink
	default:
		return linkResolverProviderDeezerSongLink
	}
}

func GetLinkResolverAllowFallback() bool {
	settings, err := LoadConfigSettings()
	if err != nil || settings == nil {
		return true
	}

	allowFallback, ok := settings["allowResolverFallback"].(bool)
	if !ok {
		return true
	}

	return allowFallback
}
