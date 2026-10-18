package http

import (
	stdErrors "errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/sjzar/chatlog/internal/errors"
	"github.com/sjzar/chatlog/pkg/util/silk"
)

func (s *Service) handleImageFile(c *gin.Context, absolutePath string) {
	// Check if the file needs decryption (either .dat extension or no extension)
	needsDecryption := strings.HasSuffix(strings.ToLower(absolutePath), ".dat") ||
		filepath.Ext(absolutePath) == ""

	// If it doesn't need decryption, redirect to the data handler
	if !needsDecryption {
		relativePath, err := s.relativeDataPath(absolutePath)
		if err != nil {
			errors.Err(c, errors.ErrMediaNotFound)
			return
		}
		c.Redirect(http.StatusFound, "/data/"+relativePath)
		return
	}

	// Determine the base path for converted files
	var outputPath string
	if filepath.Ext(absolutePath) == "" {
		// No extension, use the path as is
		outputPath = absolutePath
	} else {
		// Has .dat extension, remove it
		outputPath = strings.TrimSuffix(absolutePath, filepath.Ext(absolutePath))
	}

	var newRelativePath string
	relativePathBase, relErr := s.relativeDataPath(outputPath)
	if relErr != nil {
		errors.Err(c, errors.ErrMediaNotFound)
		return
	}

	// Check if a converted file already exists
	for _, ext := range []string{".jpg", ".png", ".gif", ".jpeg", ".bmp"} {
		if _, err := os.Stat(outputPath + ext); err == nil {
			newRelativePath = relativePathBase + ext
			break
		}
	}

	// If a converted file is found, redirect to it immediately
	if newRelativePath != "" {
		c.Redirect(http.StatusFound, "/data/"+newRelativePath)
		return
	}

	release, err := s.acquireLocalMediaSlot(c.Request.Context())
	if err != nil {
		if c.Request.Context().Err() == nil {
			errors.Err(c, err)
		}
		return
	}
	defer release()

	// Try to decrypt and convert the file
	b, err := readLocalMediaFile(absolutePath)
	if err != nil {
		if stdErrors.Is(err, errLocalMediaTooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": err.Error()})
			return
		}
		// If file doesn't exist or can't be read, fallback to redirect
		relativePath, relErr := s.relativeDataPath(absolutePath)
		if relErr != nil {
			errors.Err(c, errors.ErrMediaNotFound)
			return
		}
		c.Redirect(http.StatusFound, "/data/"+relativePath)
		return
	}

	out, ext, err := s.decodeMedia(b)
	if err != nil {
		// If decryption fails, fallback to serving the file as-is
		relativePath, relErr := s.relativeDataPath(absolutePath)
		if relErr != nil {
			errors.Err(c, errors.ErrMediaNotFound)
			return
		}
		c.Redirect(http.StatusFound, "/data/"+relativePath)
		return
	}

	// Save the decrypted file
	s.saveDecryptedFile(absolutePath, out, ext)

	// Build the new relative path and redirect
	newRelativePath = relativePathBase + "." + ext
	c.Redirect(http.StatusFound, "/data/"+newRelativePath)
}

func (s *Service) handleMediaData(c *gin.Context) {
	rawPath := strings.TrimPrefix(c.Param("path"), "/")
	absolutePath, _, err := s.safeDataPath(rawPath)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "Forbidden",
		})
		return
	}

	if _, err := os.Stat(absolutePath); os.IsNotExist(err) {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "File not found",
		})
		return
	}
	absolutePath = resolvePlainImageAttachment(s.conf.GetDataDir(), absolutePath)

	ext := strings.ToLower(filepath.Ext(absolutePath))
	switch {
	case ext == ".dat", ext == "":
		// Try to decrypt .dat files or files without extension
		s.HandleDatFile(c, absolutePath)
	default:
		// 直接返回文件
		c.File(absolutePath)
	}

}

func (s *Service) safeDataPath(input string) (absolutePath, relativePath string, err error) {
	base := filepath.Clean(s.conf.GetDataDir())
	if base == "" || base == "." {
		return "", "", errors.ErrMediaNotFound
	}

	cleaned := filepath.Clean(strings.TrimPrefix(input, "/"))
	if cleaned == "." || cleaned == "" {
		return "", "", errors.ErrMediaNotFound
	}

	absolutePath = filepath.Join(base, cleaned)
	relativePath, err = filepath.Rel(base, absolutePath)
	if err != nil {
		return "", "", errors.ErrMediaNotFound
	}
	if relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
		return "", "", errors.ErrMediaNotFound
	}
	return absolutePath, filepath.ToSlash(relativePath), nil
}

func (s *Service) relativeDataPath(absolutePath string) (string, error) {
	base := filepath.Clean(s.conf.GetDataDir())
	if base == "" || base == "." {
		return "", errors.ErrMediaNotFound
	}
	cleaned := filepath.Clean(absolutePath)
	relativePath, err := filepath.Rel(base, cleaned)
	if err != nil {
		return "", errors.ErrMediaNotFound
	}
	if relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
		return "", errors.ErrMediaNotFound
	}
	return filepath.ToSlash(relativePath), nil
}

func (s *Service) HandleDatFile(c *gin.Context, path string) {
	release, err := s.acquireLocalMediaSlot(c.Request.Context())
	if err != nil {
		if c.Request.Context().Err() == nil {
			errors.Err(c, err)
		}
		return
	}
	defer release()

	b, err := readLocalMediaFile(path)
	if err != nil {
		if stdErrors.Is(err, errLocalMediaTooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": err.Error()})
			return
		}
		errors.Err(c, err)
		return
	}
	out, ext, err := s.decodeMedia(b)
	if err != nil {
		// WeFlow-style auto self-heal:
		// on AES padding mismatch, refresh ImgKey from current WeChat process once and retry.
		if s.shouldRetryImageDecryptAfterKeyRefresh(err) {
			if refreshedKey, refreshErr := s.tryRefreshImageKeyFromWeChat(); refreshErr == nil && refreshedKey != "" {
				if out2, ext2, err2 := s.decodeMedia(b); err2 == nil {
					out, ext, err = out2, ext2, nil
				} else {
					err = err2
				}
			}
		}
	}
	if err != nil {
		// If decryption fails, check if this is a file without extension
		// If so, try to return it as-is
		if filepath.Ext(path) == "" {
			// Try to detect the file type and return appropriately
			http.DetectContentType(b)
			c.Data(http.StatusOK, http.DetectContentType(b), b)
			return
		}

		// For .dat files that fail to decrypt, return error
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":  "Failed to parse .dat file",
			"reason": err.Error(),
			"path":   path,
		})
		return
	}

	// Save decrypted file to local disk
	if s.conf.GetSaveDecryptedMedia() {
		s.saveDecryptedFile(path, out, ext)
	}

	switch ext {
	case "jpg", "jpeg":
		c.Data(http.StatusOK, "image/jpeg", out)
	case "png":
		c.Data(http.StatusOK, "image/png", out)
	case "gif":
		c.Data(http.StatusOK, "image/gif", out)
	case "bmp":
		c.Data(http.StatusOK, "image/bmp", out)
	case "mp4":
		c.Data(http.StatusOK, "video/mp4", out)
	default:
		c.Data(http.StatusOK, "image/jpg", out)
		// c.File(path)
	}
}

func (s *Service) shouldRetryImageDecryptAfterKeyRefresh(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "aes decryption failed") ||
		strings.Contains(msg, "pkcs7 padding") ||
		strings.Contains(msg, "invalid padding")
}

func (s *Service) tryRefreshImageKeyFromWeChat() (string, error) {
	s.imgKeyRefreshMu.Lock()
	if time.Since(s.lastImgKeyRefresh) < 10*time.Second {
		s.imgKeyRefreshMu.Unlock()
		return "", nil
	}
	s.lastImgKeyRefresh = time.Now()
	s.imgKeyRefreshMu.Unlock()

	if s.imageKeys == nil {
		return "", fmt.Errorf("image key provider is not configured")
	}
	imgKey, err := s.imageKeys.RefreshImageKey(s.conf.GetDataDir())
	if err != nil {
		return "", err
	}
	imgKey = strings.TrimSpace(imgKey)
	if imgKey == "" {
		return "", nil
	}

	if s.media == nil {
		return "", fmt.Errorf("media codec is not configured")
	}
	s.media.SetImageKey(imgKey)
	if s.conf.GetDataDir() != "" {
		go func() {
			if _, refreshErr := s.media.RefreshXOR(s.conf.GetDataDir()); refreshErr != nil {
				log.Debug().Err(refreshErr).Msg("media XOR refresh did not complete")
			}
		}()
	}
	log.Info().Str("img_key", imgKey).Msg("refreshed image key for media decryption retry")
	return imgKey, nil
}

func (s *Service) decodeMedia(data []byte) ([]byte, string, error) {
	if s.media == nil {
		return nil, "", fmt.Errorf("media codec is not configured")
	}
	return s.media.Decode(data)
}

func (s *Service) HandleVoice(c *gin.Context, data []byte) {
	out, err := silk.Silk2MP3(data)
	if err != nil {
		c.Data(http.StatusOK, "audio/silk", data)
		return
	}
	c.Data(http.StatusOK, "audio/mp3", out)
}

// saveDecryptedFile saves the decrypted media file to local disk
func (s *Service) saveDecryptedFile(datPath string, data []byte, ext string) {
	// Generate target file path: replace .dat with actual extension
	outputPath := strings.TrimSuffix(datPath, filepath.Ext(datPath)) + "." + ext

	// Check if file already exists to avoid duplicate writes
	if _, err := os.Stat(outputPath); err == nil {
		return
	}

	// Write file
	if err := os.WriteFile(outputPath, data, 0644); err != nil {
		log.Error().
			Err(err).
			Str("dat_path", datPath).
			Str("output_path", outputPath).
			Msg("Failed to save decrypted file")
		return
	}

	log.Debug().
		Str("dat_path", datPath).
		Str("output_path", outputPath).
		Str("format", ext).
		Int("size", len(data)).
		Msg("Decrypted file saved successfully")
}
