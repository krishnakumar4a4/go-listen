package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ricochet2200/go-disk-usage/du"
)

// Setup on local machine
// 1. Key based SSH required
// 2. SSH and SCP Commands

// Setup on remote machine
// 1. Local machine should be added to authorised keys
// 2. sha256sum command should be available

var (
	// LOCAL_DIR = go-listen-recordings
	rootDir string
	// REMOTE_HOST = rasp-pi
	remoteHostName string
	// REMOTE_HOST_DIR = ~/go-listen-recordings
	remoteDirPath string
)

func init() {
	rootDir = os.Getenv("LOCAL_DIR")
	if strings.TrimSpace(rootDir) == "" {
		log.Fatal("No LOCAL_DIR set")
	}

	remoteHostName = os.Getenv("REMOTE_HOST")
	if strings.TrimSpace(rootDir) == "" {
		log.Fatal("No REMOTE_HOST set")
	}

	remoteDirPath = os.Getenv("REMOTE_HOST_DIR")
	if strings.TrimSpace(rootDir) == "" {
		log.Fatal("No REMOTE_HOST_DIR set")
	}
}

func main() {
	wg := sync.WaitGroup{}
	if err := os.MkdirAll(rootDir, os.ModePerm); err != nil {
		log.Fatal("Unable to create directory: ", err.Error())
	}

	// create date based directories two days in advance
	createNewDirForUpcomingDays()
	wg.Add(1)
	go runCreateDirectoriesOnDayChange(&wg)

	// go routine to create new directory when day passes, runs every 12hours

	// list and SCP old directories
	wg.Add(1)
	go listAndCopyOldDirectories(rootDir, &wg)

	// execute ffmpeg command
	runFFMPEGCommand()

	// delete old folders if disk space is low
	printDiskUsage()
	wg.Wait()
}

func runCreateDirectoriesOnDayChange(wg *sync.WaitGroup) {
	defer wg.Done()
	ticker := time.NewTicker(time.Hour * 12)
	for {
		select {
		case <-ticker.C:
			createNewDirForUpcomingDays()
		}
	}
}

func createNewDirForUpcomingDays() {
	now := time.Now()
	dayDir := getDirName(now)
	nextDayDir := getDirName(now.AddDate(0, 0, 1))
	nextToNextDayDir := getDirName(now.AddDate(0, 0, 2))

	if err := os.MkdirAll(filepath.Join(rootDir, dayDir), os.ModePerm); err != nil {
		log.Fatal("unable to create directory: ", err.Error())
	}

	if err := os.MkdirAll(filepath.Join(rootDir, nextDayDir), os.ModePerm); err != nil {
		log.Fatal("unable to create directory: ", err.Error())
	}

	if err := os.MkdirAll(filepath.Join(rootDir, nextToNextDayDir), os.ModePerm); err != nil {
		log.Fatal("unable to create directory: ", err.Error())
	}
}

func runFFMPEGCommand() {
	// ffmpeg -f alsa -ac 2 -ar 48000 -i plughw:1 -map 0:0 -acodec libmp3lame   -b:a 96k -f segment -strftime 1 -segment_time 120 -segment_atclocktime 1 %Y%m%d/%H-%M-%S.mp3
	cmd := exec.Command("ffmpeg", "-f", "alsa", "-ac", "2", "-ar", "48000", "-i", "plughw:1", "-map", "0:0", "-acodec", "libmp3lame", "-b:a", "96k", "-f", "segment", "-strftime", "1", "-segment_time", "120", "-segment_atclocktime", "1", filepath.Join(rootDir, "%Y%m%d/%H-%M-%S.mp3"))
	wg := &sync.WaitGroup{}
	stdOutReader, err := cmd.StdoutPipe()
	if err != nil {
		log.Fatalf("unable to open stdout pipe: %s", err.Error())
		return
	}
	wg.Add(1)
	go logStdOut(wg, stdOutReader)

	stdErrReader, err := cmd.StderrPipe()
	if err != nil {
		log.Fatalf("unable to open stderr pipe: %s", err.Error())
		return
	}
	wg.Add(1)
	go logStdErr(wg, stdErrReader)

	if err := cmd.Run(); err != nil {
		log.Fatal("error running ffmpeg command", err.Error())
	}
	wg.Wait()
}

func logStdOut(wg *sync.WaitGroup, readCloser io.ReadCloser) {
	defer readCloser.Close()
	defer wg.Done()

	fileScanner := bufio.NewScanner(readCloser)
	fileScanner.Split(bufio.ScanLines)

	for fileScanner.Scan() {
		log.Println("STDOUT: " + fileScanner.Text())
	}

	if err := fileScanner.Err(); err != nil {
		log.Printf("unable to command stdout: %s \n", err.Error())
		return
	}
}

func logStdErr(wg *sync.WaitGroup, readCloser io.ReadCloser) {
	defer readCloser.Close()
	defer wg.Done()

	fileScanner := bufio.NewScanner(readCloser)
	fileScanner.Split(bufio.ScanLines)

	for fileScanner.Scan() {
		log.Println("STDERR: " + fileScanner.Text())
	}

	if err := fileScanner.Err(); err != nil {
		log.Printf("unable to command stderr: %s \n", err.Error())
		return
	}
}

func listAndCopyOldDirectories(rootDir string, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		// list directory
		folders, err := os.ReadDir(rootDir)
		if err != nil {
			log.Fatal("unable to read root directory: ", err.Error())
		}

		folderNames := make([]string, 0, len(folders))
		for _, folder := range folders {
			folderNames = append(folderNames, folder.Name())
		}
		sort.Strings(folderNames)

		t := time.Now()
		dayDirName := getDirName(t)

		for _, folderName := range folderNames {
			// skip today and subsequent days dirs
			if folderName >= dayDirName {
				log.Println("reached today's directory, skipping copy")
				break
			}

			// verify before copying folder
			log.Printf("started veryfying %s before copy to remote\n", folderName)
			verify := verifyFilesWithDestination(rootDir, folderName)
			log.Printf("verifyFilesWithDestination for %s is: %v \n", folderName, verify)

			if !verify {
				// scp files
				log.Printf("started copying %s \n", folderName)

				// rasp-pi:/path-to-recordings
				remoteHostPath := fmt.Sprintf("%s:%s", remoteHostName, filepath.Join(remoteDirPath))
				// scp -rv /local-dir-for-recordings rasp-pi:/path-to-recordings
				scpCmdStdOut, err := exec.Command("scp", "-rv", filepath.Join(rootDir, folderName), remoteHostPath).Output()
				if err != nil {
					log.Fatal("unable to copy folder to remote using scp: ", err.Error())
				}
				log.Printf("successfully copied %s folder to remote\n", folderName)
				log.Println("scp output", scpCmdStdOut)

				// verify copied folder
				log.Printf("started veryfying %s after copy to remote\n", folderName)
				verify = verifyFilesWithDestination(rootDir, folderName)
				log.Printf("verifyFilesWithDestination for %s is: %v \n", folderName, verify)

			}

			// delete source files
			if verify {
				log.Printf("deleting local folder %s as its copied to remote and verified", folderName)
				if err := os.RemoveAll(filepath.Join(rootDir, folderName)); err != nil {
					log.Printf("failed to local folder %s after its copied to remote and verified", folderName)
				}
			}
		}

		time.Sleep(time.Minute * 30)
	}
}

func getDirName(t time.Time) string {
	return fmt.Sprintf("%04d%02d%02d", t.Year(), t.Month(), t.Day())
}

func verifyFilesWithDestination(rootDir, folderName string) bool {
	// check if folder exists in destination
	var (
		ee *exec.ExitError
		pe *os.PathError
	)

	// /local-dir-for-recordings/20230210
	remoteFolderPath := filepath.Join(remoteDirPath, folderName)

	// ssh rasp-pi:/path-to-recordings ls /local-dir-for-recordings/20230210
	cmd := exec.Command("ssh", remoteHostName, "ls", remoteFolderPath)
	if err := cmd.Run(); err != nil {
		if errors.As(err, &ee) {
			log.Println("exit code error: ", ee.ExitCode()) // ran, but non-zero exit code
			return false
		} else if errors.As(err, &pe) {
			log.Printf("os.PathError: %v\n", pe) // "no such file ...", "permission denied" etc.
			return false
		}
		log.Fatal("unable to run folder exists check command on remote: ", err.Error())
		return false
	}
	log.Printf("dir %s exists on remote\n", folderName)

	files, err := os.ReadDir(filepath.Join(rootDir, folderName))
	if err != nil {
		log.Fatal("unable to read root directory: ", err.Error())
	}

	fileNames := make([]string, 0, len(files))
	for _, f := range files {
		fileNames = append(fileNames, f.Name())
	}
	sort.Strings(fileNames)

	for _, fName := range fileNames {
		openedFile, err := os.Open(filepath.Join(rootDir, folderName, fName))
		if err != nil {
			log.Fatal("unable to read file in directory: ", err.Error())
		}
		content, err := io.ReadAll(openedFile)
		if err != nil {
			log.Fatal("unable to read content in file: ", err.Error())
		}
		srcFileSHA := sha256.Sum256(content)
		log.Printf("src file %s sha: %v \n", fName, srcFileSHA)

		// check dest file sha

		// /local-dir-for-recordings/20230210/10-33-22.mp3
		remoteFolderPath := filepath.Join(remoteDirPath, folderName, fName)

		// ssh rasp-pi:/path-to-recordings sha256sum /local-dir-for-recordings/20230210/
		out, err := runCmdOnRemote("sha256sum", remoteFolderPath)
		if err != nil {
			log.Fatal("unable to run sha256 command on remote file: ", err.Error())
		}
		log.Println("output from remote: ", out)
		fileSha := strings.Split(out, " ")[0]

		sha256Bytes, err := hex.DecodeString(fileSha)
		if err != nil {
			log.Fatal("unable to decode hex encoded sha256 of remote file: ", err.Error())
		}
		log.Printf("remote file %s sha: %v \n", fName, sha256Bytes)

		if bytes.Equal(srcFileSHA[:], sha256Bytes) {
			log.Println("local file and remote file has same sha")
		} else {
			return false
		}
		// if diff return false
	}
	return true
}

func runCmdOnRemote(cmdWithParams ...string) (string, error) {
	var (
		ee *exec.ExitError
		pe *os.PathError
	)

	finalCmdParams := make([]string, 0, 1+len(cmdWithParams))
	finalCmdParams = append(finalCmdParams, remoteHostName)
	finalCmdParams = append(finalCmdParams, cmdWithParams...)

	cmd := exec.Command("ssh", finalCmdParams...)
	stdOut, err := cmd.Output()
	if err != nil {
		if errors.As(err, &ee) {
			log.Println("exit code error: ", ee.ExitCode()) // ran, but non-zero exit code
			return "", err
		} else if errors.As(err, &pe) {
			log.Printf("os.PathError: %v\n", pe) // "no such file ...", "permission denied" etc.
			return "", err
		}
		log.Fatal("unable to run folder exists check command on remote: ", err.Error())
		return "", err
	}
	return string(stdOut), nil
}

func printDiskUsage() {
	usage := du.NewDiskUsage("/")
	fmt.Println("usage avail: ", usage.Available())
	fmt.Println("usage free: ", usage.Free())
	fmt.Println("usage size: ", usage.Size())
	fmt.Println("usage usage: ", usage.Usage())
	fmt.Println("usage used: ", usage.Used())
}
