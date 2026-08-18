# Define the valid Go target platforms
$Platforms = @(
    "linux/amd64",
    "linux/arm64",
    "linux/386",
    "linux/arm",

    "windows/amd64",
    "windows/386",

    # macOS (Only supports 64-bit architectures)
    "darwin/amd64",
    "darwin/arm64"
)

foreach ($Platform in $Platforms) {
    # Split the "os/arch" string
    $Split = $Platform.Split("/")
    $env:GOOS = $Split[0]
    $env:GOARCH = $Split[1]

    # Define the output filename
    $OutputName = "webcrawler-$($env:GOOS)-$($env:GOARCH)"
    if ($env:GOOS -eq "windows") {
        $OutputName += ".exe"
    }

    Write-Host "Compiling for $($env:GOOS)/$($env:GOARCH)..." -ForegroundColor Cyan

    # Execute the Go build command
    go build -o $OutputName ./cmd/crawler/main.go

    # Check if the build command succeeded
    if ($LASTEXITCODE -ne 0) {
        Write-Error "An error occurred! Aborting the build process."

        # Clean up environment variables before exiting
        Remove-Item env:GOOS -ErrorAction SilentlyContinue
        Remove-Item env:GOARCH -ErrorAction SilentlyContinue
        exit 1
    }

    if ($env:GOOS -eq "windows") {
        # Unblock Windows binaries locally
        Unblock-File -Path ".\$OutputName" -ErrorAction SilentlyContinue
    } else {
        # If running inside WSL or Git Bash on Windows, try to apply chmod +x
        if (Get-Command wsl -ErrorAction SilentlyContinue) {
            wsl chmod +x "./$OutputName" 2>$null
        } elseif (Get-Command chmod -ErrorAction SilentlyContinue) {
            chmod +x "./$OutputName" 2>$null
        }
    }
}

# Clean up environment variables after successful execution
Remove-Item env:GOOS -ErrorAction SilentlyContinue
Remove-Item env:GOARCH -ErrorAction SilentlyContinue

Write-Host "All builds completed successfully!" -ForegroundColor Green
