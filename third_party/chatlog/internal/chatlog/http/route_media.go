package http

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/sjzar/chatlog/internal/errors"
	"github.com/sjzar/chatlog/pkg/util"
)

func (s *Service) handleMedia(c *gin.Context, _type string) {
	key := strings.TrimPrefix(c.Param("key"), "/")
	if key == "" {
		errors.Err(c, errors.InvalidArg(key))
		return
	}

	keys := util.Str2List(key, ",")
	if len(keys) == 0 {
		errors.Err(c, errors.InvalidArg(key))
		return
	}

	var _err error
	for _, k := range keys {
		if strings.Contains(k, "/") {
			if absolutePath, err := s.findPath(_type, k); err == nil {
				if _type == "image" {
					s.handleImageFile(c, filepath.Join(s.conf.GetDataDir(), absolutePath))
					return
				}
				if _type == "file" {
					resolved := resolvePlainImageAttachment(s.conf.GetDataDir(), filepath.Join(s.conf.GetDataDir(), absolutePath))
					if relative, relErr := s.relativeDataPath(resolved); relErr == nil {
						absolutePath = relative
					}
				}
				c.Redirect(http.StatusFound, "/data/"+absolutePath)
				return
			}
		}
		media, err := s.db.GetMedia(_type, k)
		if err != nil {
			// Fallback 1: try to find path from md5->path cache
			if cachedPath := s.getMD5FromCache(k); cachedPath != "" {
				// Try to find the actual file with different suffixes
				if absolutePath := s.tryFindFileWithSuffixes(_type, cachedPath); absolutePath != "" {
					if _type == "image" {
						s.handleImageFile(c, absolutePath)
						return
					}
					relativePath, relErr := s.relativeDataPath(absolutePath)
					if relErr == nil {
						c.Redirect(http.StatusFound, "/data/"+relativePath)
						return
					}
					return
				}
			}

			// Fallback 2: try to find file by md5 in msg/attach directory
			if _type == "image" && !strings.Contains(k, "/") {
				// Build md5->path map from recent messages on demand if cache is cold.
				if cachedPath := s.resolveImagePathFromRecentMessages(k); cachedPath != "" {
					if absolutePath := s.tryFindFileWithSuffixes("image", cachedPath); absolutePath != "" {
						s.handleImageFile(c, absolutePath)
						return
					}
				}
				if foundPath := s.findImageByMD5(k); foundPath != "" {
					// Process the found image file
					s.handleImageFile(c, foundPath)
					return
				}
			}

			_err = err
			continue
		}
		if c.Query("info") != "" {
			c.JSON(http.StatusOK, media)
			return
		}
		switch media.Type {
		case "voice":
			s.HandleVoice(c, media.Data)
			return
		case "image":
			s.rememberMD5Path(k, media.Path)
			absolutePath := s.tryFindFileWithSuffixes("image", media.Path)
			if absolutePath == "" {
				absolutePath = s.findRelocatedMediaFile(media)
			}
			if absolutePath == "" {
				absolutePath = s.findImageByMD5(k)
			}
			if absolutePath == "" {
				_err = errors.ErrMediaNotFound
				continue
			}
			if relativePath, relErr := s.relativeDataPath(absolutePath); relErr == nil {
				s.rememberMD5Path(k, relativePath)
			}
			s.handleImageFile(c, absolutePath)
			return
		default:
			absolutePath := s.tryFindFileWithSuffixes(media.Type, media.Path)
			if absolutePath == "" {
				absolutePath = s.findRelocatedMediaFile(media)
			}
			if absolutePath == "" {
				_err = errors.ErrMediaNotFound
				continue
			}
			if media.Type == "file" {
				absolutePath = resolvePlainImageAttachment(s.conf.GetDataDir(), absolutePath)
			}
			relativePath, relErr := s.relativeDataPath(absolutePath)
			if relErr != nil {
				_err = errors.ErrMediaNotFound
				continue
			}
			s.rememberMD5Path(k, relativePath)
			c.Redirect(http.StatusFound, "/data/"+relativePath)
			return
		}
	}

	if _err != nil {
		errors.Err(c, _err)
		return
	}
}

func (s *Service) findPath(_type string, key string) (string, error) {
	absolutePath, relativePath, err := s.safeDataPath(key)
	if err != nil {
		return "", errors.ErrMediaNotFound
	}
	if _, err := os.Stat(absolutePath); err == nil {
		return relativePath, nil
	}
	switch _type {
	case "image":
		for _, suffix := range imageDATSuffixesByPriority() {
			candidate := absolutePath + suffix
			if _, err := os.Stat(candidate); err == nil {
				if rel, relErr := s.relativeDataPath(candidate); relErr == nil {
					return rel, nil
				}
			}
		}
	case "video":
		for _, suffix := range []string{".mp4", "_thumb.jpg"} {
			candidate := absolutePath + suffix
			if _, err := os.Stat(candidate); err == nil {
				if rel, relErr := s.relativeDataPath(candidate); relErr == nil {
					return rel, nil
				}
			}
		}
	}
	return "", errors.ErrMediaNotFound
}
