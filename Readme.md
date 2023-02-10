# GO Listener

A golang program to constantly listen for USB mic audio and transport generated files to a remote destination for safe keeping via SSH

## How to run
```LOCAL_DIR=go-listen-recordings REMOTE_HOST=rasp-pi REMOTE_HOST_DIR=~/go-listen-recordings go run main.go```

## Execution flow
1. Creates directory for current date and next couple of days ahead of time
2. List old directories (older than todays date)
3. Check if old directory already exists on remote destination
4. If directory exists, check if all the files in it match check sum with local
5. If folder does not exist or checksum mismatch, copy entire folder again
6. Post copy, do verification of remote folder again and if successful, delete the local folder
7. If all directories older than today is copied to remote host, wait for 30mins and goes to step 1