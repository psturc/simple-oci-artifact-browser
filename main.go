package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Maximum size for decompressed .gz files (50MB) to prevent decompression bombs
const maxDecompressedSize = 50 * 1024 * 1024

const timeInputLayout = "Mon, 02 Jan 2006 15:04:05 -0700"

type FileData struct {
	Name    string
	Path    string
	IsDir   bool
	Size    int64
	ModTime time.Time
}

type DirectoryListing struct {
	Files        []FileData
	ParentPath   string
	SyncInterval string
	Repositories []Repository
}

type Repository struct {
	Name string
	Dir  string
}

type TagResponse struct {
	Tags []Tag `json:"tags"`
}
type Tag struct {
	Name         string `json:"name"`
	LastModified string `json:"last_modified"`
}

// Base directory to serve files from
var baseDir string = "./files"

var quayOrgAndRepos string = os.Getenv("QUAY_ORG_REPOS")
var port string = os.Getenv("PORT")
var syncIntervalEnvValue string = os.Getenv("SYNC_INTERVAL_MINUTES")
var syncInterval int

var repositories = []Repository{}

func main() {
	var err error

	if quayOrgAndRepos == "" {
		log.Fatal("QUAY_ORG_REPO env var is empty")
	}
	if syncIntervalEnvValue == "" {
		log.Println("SYNC_INTERVAL_MINUTES env var is empty, setting default value to 1 minute")
		syncIntervalEnvValue = "1"
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)

	syncInterval, err = strconv.Atoi(syncIntervalEnvValue)
	if err != nil {
		log.Println("env var SYNC_INTERVAL_MINUTES value is invalid, setting default value to 1 minute")
		syncInterval = 1
	}

	repos := strings.Split(quayOrgAndRepos, ",")
	for _, repo := range repos {
		trimmedspace := strings.TrimSpace(repo)
		repoNameSplit := strings.Split(repo, "/")
		dirName := repoNameSplit[len(repoNameSplit)-1]

		repositories = append(repositories, Repository{Name: trimmedspace, Dir: dirName})
	}

	go func() {
		if err := orasPull(); err != nil {
			log.Printf("ERROR: %s\n", err)
		}
		for range time.Tick(time.Duration(syncInterval) * time.Minute) {
			if err := orasPull(); err != nil {
				log.Printf("ERROR: %s\n", err)
			}
		}
	}()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("DEBUG: Request URL path: %q", r.URL.Path)

		// Clean up the URL path (normalize it, remove trailing slashes)
		requestPath := filepath.Clean(filepath.Join(baseDir, r.URL.Path))
		log.Printf("DEBUG: Resolved file path: %q", requestPath)

		// Get file info for the requested path
		fileInfo, err := os.Stat(requestPath)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		// If the requested path is a directory, list its contents
		log.Printf("DEBUG: IsDir=%v for path %q", fileInfo.IsDir(), requestPath)
		if fileInfo.IsDir() {
			files, err := os.ReadDir(requestPath)
			if err != nil {
				http.Error(w, "Unable to read directory", http.StatusInternalServerError)
				return
			}

			// Prepare data for the template
			fileDataList := []FileData{}
			for _, file := range files {
				filePath := filepath.Join(r.URL.Path, file.Name())
				info, _ := file.Info() // Fetch file info for metadata

				filename := file.Name()
				modtime := info.ModTime()

				fileDataList = append(fileDataList, FileData{
					Name:    filename,
					Path:    filePath,
					IsDir:   file.IsDir(),
					Size:    info.Size(), // File size
					ModTime: modtime,     // Last modification time
				})
			}

			// Sort files: directories first, then files
			sort.SliceStable(fileDataList, func(i, j int) bool {
				if fileDataList[i].IsDir && fileDataList[j].IsDir {
					return fileDataList[i].ModTime.After(fileDataList[j].ModTime)
				}
				if fileDataList[i].IsDir && !fileDataList[j].IsDir {
					return true
				}
				if !fileDataList[i].IsDir && fileDataList[j].IsDir {
					return false
				}
				return fileDataList[i].Name < fileDataList[j].Name
			})

			// Calculate parent path (for ".." functionality)
			var parentPath string
			if r.URL.Path != "/" {
				// Get parent directory and ensure it does not contain baseDir
				parentPath = filepath.Clean(filepath.Join(r.URL.Path, ".."))
				if parentPath == "." {
					parentPath = "/"
				}
			}

			// Parse and execute the template
			tmpl := template.Must(template.ParseFiles("templates/index.html"))
			tmpl.Execute(w, DirectoryListing{
				Files:        fileDataList,
				ParentPath:   parentPath,
				SyncInterval: syncIntervalEnvValue,
				Repositories: repositories,
			})
		} else {
			// If it's a file, serve the file directly
			// Note: http.ServeFile redirects /path/index.html to /path/, which breaks
			// serving index.html files. Read and write content directly instead.

			var content []byte
			var err error
			ext := strings.ToLower(filepath.Ext(requestPath))

			// For .gz files, decompress and serve the content
			if ext == ".gz" {
				file, err := os.Open(requestPath)
				if err != nil {
					http.Error(w, "Unable to open file", http.StatusInternalServerError)
					return
				}
				defer file.Close()

				gzReader, err := gzip.NewReader(file)
				if err != nil {
					http.Error(w, "Unable to decompress file", http.StatusInternalServerError)
					return
				}
				defer gzReader.Close()

				// Limit decompressed size to prevent decompression bombs
				limitedReader := io.LimitReader(gzReader, maxDecompressedSize+1)
				content, err = io.ReadAll(limitedReader)
				if err != nil {
					http.Error(w, "Unable to read decompressed content", http.StatusInternalServerError)
					return
				}
				if len(content) > maxDecompressedSize {
					http.Error(w, "Decompressed file too large (max 50MB)", http.StatusRequestEntityTooLarge)
					return
				}

				// Get the extension of the inner file (without .gz)
				innerName := strings.TrimSuffix(filepath.Base(requestPath), ".gz")
				ext = strings.ToLower(filepath.Ext(innerName))
			} else {
				content, err = os.ReadFile(requestPath)
				if err != nil {
					http.Error(w, "Unable to read file", http.StatusInternalServerError)
					return
				}
			}

			// Detect content type from filename
			// Default to text/plain so unknown file types are viewable in the browser
			contentType := "text/plain; charset=utf-8"
			switch ext {
			case ".html", ".htm":
				contentType = "text/html; charset=utf-8"
			case ".css":
				contentType = "text/css; charset=utf-8"
			case ".js":
				contentType = "application/javascript"
			case ".json":
				contentType = "application/json"
			case ".xml", ".junit":
				contentType = "text/xml; charset=utf-8"
			case ".png":
				contentType = "image/png"
			case ".jpg", ".jpeg":
				contentType = "image/jpeg"
			case ".gif":
				contentType = "image/gif"
			case ".webp":
				contentType = "image/webp"
			case ".svg":
				contentType = "image/svg+xml"
			case ".pdf":
				contentType = "application/pdf"
			case ".zip", ".tar", ".tgz":
				contentType = "application/octet-stream"
			}

			w.Header().Set("Content-Type", contentType)
			w.Header().Set("Content-Length", strconv.Itoa(len(content)))
			w.Write(content)
		}
	})

	log.Printf("Serving files on port %s\n", port)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%s", port), nil))
}

func orasPull() error {
	for _, repo := range repositories {
		url := fmt.Sprintf("https://quay.io/api/v1/repository/%s/tag/", repo.Name)
		log.Printf("going to pull latest artifacts from: %s", url)
		res, err := http.Get(url)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("got unexpected status from quay repo %s: %d", url, res.StatusCode)
		}

		body, err := io.ReadAll(res.Body)
		if err != nil {
			return fmt.Errorf("cannot read body of a response from quay.io regarding %s %+v", url, err)
		}
		tagResponse := &TagResponse{}
		if err = json.Unmarshal(body, tagResponse); err != nil {
			return fmt.Errorf("failed to unmarshal response from quay.io regarding regarding %s %+v", url, err)
		}
		if len(tagResponse.Tags) < 1 {
			return fmt.Errorf("cannot get manifest digest regarding %s %+v", url, err)
		}

		cwd, _ := os.Getwd()
		filesPath := filepath.Clean(filepath.Join(cwd, baseDir))

		for _, tag := range tagResponse.Tags {
			tagRef := fmt.Sprintf("quay.io/%s:%s", repo.Name, tag.Name)
			outputPath := filepath.Clean(filepath.Join(filesPath, repo.Dir, tag.Name))
			tagLastModified, err := time.Parse(timeInputLayout, tag.LastModified)
			if err != nil {
				log.Printf("error parsing time for last modified: %+v\n", err)
			}
			dirInfo, err := os.Stat(outputPath)
			// Directory with artifacts already exists
			if err == nil {
				if !tagLastModified.After(dirInfo.ModTime()) {
					continue
				}
				log.Printf("got newer content for for %s!(tag last modified: %s, dir last modified %s)\n", tagRef, tagLastModified, dirInfo.ModTime().Format(timeInputLayout))
				err := os.RemoveAll(outputPath)
				if err != nil {
					log.Printf("failed to remove the directory %s: %+v", outputPath, err)
					continue
				}
			}
			if err := os.MkdirAll(outputPath, 0700); err != nil {
				return err
			}

			app := "oras"
			args := []string{"pull", tagRef, "--output", fmt.Sprintf("%s", outputPath)}
			cmd := exec.Command(app, args...)
			if err := cmd.Run(); err != nil {
				return err
			}
			err = os.Chtimes(outputPath, tagLastModified, tagLastModified)
			if err != nil {
				log.Printf("failed to change the mod time for the directory %s: %+v", outputPath, err)
			}
		}
	}
	return nil
}
