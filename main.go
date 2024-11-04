package main

import (
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

		// Clean up the URL path (normalize it, remove trailing slashes)
		requestPath := filepath.Clean(filepath.Join(baseDir, r.URL.Path))

		// Get file info for the requested path
		fileInfo, err := os.Stat(requestPath)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		// If the requested path is a directory, list its contents
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

				fileDataList = append(fileDataList, FileData{
					Name:    file.Name(),
					Path:    filePath,
					IsDir:   file.IsDir(),
					Size:    info.Size(),    // File size
					ModTime: info.ModTime(), // Last modification time
				})
			}

			// Sort files: directories first, then files
			sort.SliceStable(fileDataList, func(i, j int) bool {
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
			// If it's a file, serve the file
			http.ServeFile(w, r, requestPath)
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
		log.Println(cwd)

		for _, tag := range tagResponse.Tags {
			prefix := getDatetimePrefix(tag.LastModified)
			outputPath := filepath.Clean(filepath.Join(filesPath, repo.Dir, prefix+"_"+tag.Name))
			_, err := os.Stat(outputPath)
			if err == nil {
				continue
			}
			if err := os.MkdirAll(outputPath, 0700); err != nil {
				return err
			}
			app := "oras"
			args := []string{"pull", fmt.Sprintf("quay.io/%s:%s", repo.Name, tag.Name), "--output", fmt.Sprintf("%s", outputPath)}
			cmd := exec.Command(app, args...)
			if err := cmd.Run(); err != nil {
				return err
			}
		}
	}
	return nil
}

// exists returns whether the given file or directory exists
func exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func getDatetimePrefix(input string) string {
	// Layout of the input datetime string
	inputLayout := "Mon, 02 Jan 2006 15:04:05 -0700"

	// Parse the input datetime string into a time.Time object
	t, err := time.Parse(inputLayout, input)
	if err != nil {
		log.Println("error parsing time:", err)
		return ""
	}

	// Desired output layout in the format "YYYY-MM-DD-HH-MM-SS"
	outputLayout := "2006-01-02_15-04-05"

	// Format the parsed time to the desired output format
	return t.Format(outputLayout)
}
