package main

import (
	"encoding/json"

	"github.com/afkarxyz/SpotiFLAC/backend"
)

func (a *App) GetDownloadedAlbums(limit int) (string, error) {
	albums, err := backend.LoadDownloadedFolders(limit)
	if err != nil {
		return "", err
	}

	data, err := json.Marshal(albums)
	if err != nil {
		return "", err
	}

	return string(data), nil
}
