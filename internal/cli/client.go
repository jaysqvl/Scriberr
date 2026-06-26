package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type resumableCreateResponse struct {
	ID        string                  `json:"id"`
	Status    string                  `json:"status"`
	Token     string                  `json:"token"`
	ChunkSize int64                   `json:"chunk_size"`
	Files     []resumableFileResponse `json:"files"`
}

type resumableFileResponse struct {
	ID             string `json:"id"`
	ChunkCount     int    `json:"chunk_count"`
	AcceptedChunks []int  `json:"accepted_chunks"`
}

type cachedUploadSession struct {
	ID          string `json:"id"`
	Token       string `json:"token"`
	ChunkSize   int64  `json:"chunk_size"`
	Fingerprint string `json:"fingerprint"`
}

// UploadFile uploads a file to the Scriberr server
func UploadFile(filePath string) error {
	config := GetConfig()
	if config.ServerURL == "" {
		return fmt.Errorf("server URL not configured. Please run 'scriberr login' or 'scriberr install'")
	}
	if config.Token == "" {
		return fmt.Errorf("not logged in (token missing). Please run 'scriberr login'")
	}

	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("failed to stat file: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("cannot upload directory: %s", filePath)
	}

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	session, err := getOrCreateCLIUploadSession(config, filePath, info)
	if err != nil {
		return err
	}

	status, err := fetchCLIUploadStatus(config, session.ID)
	if err == nil && status.ChunkSize > 0 {
		session.ChunkSize = status.ChunkSize
	}
	accepted := map[int]bool{}
	if err == nil && len(status.Files) > 0 {
		for _, chunk := range status.Files[0].AcceptedChunks {
			accepted[chunk] = true
		}
	}

	chunkCount := int((info.Size() + session.ChunkSize - 1) / session.ChunkSize)
	buffer := make([]byte, session.ChunkSize)
	for index := 0; index < chunkCount; index++ {
		if accepted[index] {
			continue
		}

		start := int64(index) * session.ChunkSize
		endExclusive := start + session.ChunkSize
		if endExclusive > info.Size() {
			endExclusive = info.Size()
		}
		size := endExclusive - start
		chunk := buffer[:size]

		if _, err := file.Seek(start, io.SeekStart); err != nil {
			return fmt.Errorf("failed to seek file: %w", err)
		}
		if _, err := io.ReadFull(file, chunk); err != nil {
			return fmt.Errorf("failed to read chunk %d: %w", index, err)
		}

		if err := uploadCLIChunk(config, session, index, start, endExclusive, info.Size(), chunk); err != nil {
			return err
		}
	}

	if err := completeCLIUpload(config, session); err != nil {
		return err
	}
	_ = removeCachedUploadSession(session.Fingerprint)
	return nil
}

func getOrCreateCLIUploadSession(config *Config, filePath string, info os.FileInfo) (*cachedUploadSession, error) {
	fingerprint := cliUploadFingerprint(config.ServerURL, filePath, info)
	if cached, err := readCachedUploadSession(fingerprint); err == nil && cached != nil {
		if status, err := fetchCLIUploadStatus(config, cached.ID); err == nil && status.Status == "active" {
			return cached, nil
		}
		_ = removeCachedUploadSession(fingerprint)
	}

	body, err := json.Marshal(map[string]interface{}{
		"kind":  "audio",
		"title": filepath.Base(filePath),
		"files": []map[string]interface{}{
			{
				"id":            "audio",
				"role":          "audio",
				"name":          filepath.Base(filePath),
				"content_type":  "application/octet-stream",
				"size":          info.Size(),
				"last_modified": info.ModTime().UnixMilli(),
			},
		},
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/transcription/uploads", config.ServerURL), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	setCLIAuth(req, config)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to create upload session: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to create upload session with status %d: %s", resp.StatusCode, string(respBody))
	}

	var created resumableCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return nil, err
	}
	if created.Token == "" || created.ChunkSize <= 0 {
		return nil, fmt.Errorf("server returned an invalid upload session")
	}

	session := &cachedUploadSession{
		ID:          created.ID,
		Token:       created.Token,
		ChunkSize:   created.ChunkSize,
		Fingerprint: fingerprint,
	}
	_ = writeCachedUploadSession(session)
	return session, nil
}

func fetchCLIUploadStatus(config *Config, sessionID string) (*resumableCreateResponse, error) {
	req, err := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/transcription/uploads/%s", config.ServerURL, sessionID), nil)
	if err != nil {
		return nil, err
	}
	setCLIAuth(req, config)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upload status failed with status %d", resp.StatusCode)
	}
	var status resumableCreateResponse
	return &status, json.NewDecoder(resp.Body).Decode(&status)
}

func uploadCLIChunk(config *Config, session *cachedUploadSession, index int, start, endExclusive, totalSize int64, chunk []byte) error {
	sum := sha256.Sum256(chunk)
	url := fmt.Sprintf("%s/api/v1/transcription/uploads/%s/files/audio/chunks/%d", config.ServerURL, session.ID, index)
	contentRange := fmt.Sprintf("bytes %d-%d/%d", start, endExclusive-1, totalSize)
	chunkHash := hex.EncodeToString(sum[:])

	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		req, err := http.NewRequest("PUT", url, bytes.NewReader(chunk))
		if err != nil {
			return err
		}
		setCLIAuth(req, config)
		req.Header.Set("Content-Range", contentRange)
		req.Header.Set("X-Upload-Token", session.Token)
		req.Header.Set("X-Chunk-SHA256", chunkHash)
		req.ContentLength = int64(len(chunk))

		resp, err := http.DefaultClient.Do(req)
		if err == nil && resp != nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("chunk %d failed with status %d: %s", index, resp.StatusCode, string(body))
		} else if err != nil {
			lastErr = err
		}
		time.Sleep(time.Duration(1<<attempt) * time.Second)
	}
	return lastErr
}

func completeCLIUpload(config *Config, session *cachedUploadSession) error {
	req, err := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/transcription/uploads/%s/complete", config.ServerURL, session.ID), nil)
	if err != nil {
		return err
	}
	setCLIAuth(req, config)
	req.Header.Set("X-Upload-Token", session.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("complete upload failed with status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func setCLIAuth(req *http.Request, config *Config) {
	req.Header.Set("Authorization", "Bearer "+config.Token)
}

func cliUploadFingerprint(serverURL, filePath string, info os.FileInfo) string {
	sum := sha256.Sum256([]byte(serverURL + "|" + filePath + "|" + strconv.FormatInt(info.Size(), 10) + "|" + strconv.FormatInt(info.ModTime().UnixNano(), 10)))
	return hex.EncodeToString(sum[:])
}

func cliUploadCachePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", err
		}
		dir = home
	}
	path := filepath.Join(dir, "scriberr")
	if err := os.MkdirAll(path, 0700); err != nil {
		return "", err
	}
	return filepath.Join(path, "upload-sessions.json"), nil
}

func readCachedUploadSession(fingerprint string) (*cachedUploadSession, error) {
	cache, err := readUploadSessionCache()
	if err != nil {
		return nil, err
	}
	session, ok := cache[fingerprint]
	if !ok {
		return nil, nil
	}
	return &session, nil
}

func writeCachedUploadSession(session *cachedUploadSession) error {
	cache, _ := readUploadSessionCache()
	if cache == nil {
		cache = map[string]cachedUploadSession{}
	}
	cache[session.Fingerprint] = *session
	return writeUploadSessionCache(cache)
}

func removeCachedUploadSession(fingerprint string) error {
	cache, err := readUploadSessionCache()
	if err != nil {
		return err
	}
	delete(cache, fingerprint)
	return writeUploadSessionCache(cache)
}

func readUploadSessionCache() (map[string]cachedUploadSession, error) {
	path, err := cliUploadCachePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]cachedUploadSession{}, nil
	}
	if err != nil {
		return nil, err
	}
	var cache map[string]cachedUploadSession
	if err := json.Unmarshal(data, &cache); err != nil {
		return map[string]cachedUploadSession{}, nil
	}
	return cache, nil
}

func writeUploadSessionCache(cache map[string]cachedUploadSession) error {
	path, err := cliUploadCachePath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}
