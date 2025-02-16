#!/bin/bash

SOURCE_DIR="/home/sawa/mainShare/devenv/gitrepos/Olympus/"
DEST_DIR="/home/sawa/Olympus"

# Use rsync to copy only new or changed files
rsync -av --update "$SOURCE_DIR" "$DEST_DIR"

# Check if Go is installed
if ! command -v go &> /dev/null; then
    echo "Go could not be found. Installing Go..."
    sudo apt install golang-go -y
fi

# Change directory and run the Go project
cd "$DEST_DIR" || exit
go run .
