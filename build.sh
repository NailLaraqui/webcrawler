#!/usr/bin/bash

platforms=(
    "linux/amd64"
    "linux/arm64"
    "linux/386"
    "linux/arm"

    "windows/amd64"
    "windows/386"

    # macOS (unfortunately no 32-bit support for macOS)
    "darwin/amd64"
    "darwin/arm64"
)

for platform in "${platforms[@]}"
do
    platform_split=(${platform//\// })
    GOOS=${platform_split[0]}
    GOARCH=${platform_split[1]}

    output_name="webcrawler-${GOOS}-${GOARCH}"
    if [ $GOOS = "windows" ]; then
        output_name+='.exe'
    fi

    echo "Compiling for ${GOOS}/${GOARCH}..."
    env GOOS=$GOOS GOARCH=$GOARCH go build -o $output_name ./cmd/crawler/main.go

    if [ $? -ne 0 ]; then
            echo "An error occurred! Aborting the build process."
            exit 1
    fi
done
