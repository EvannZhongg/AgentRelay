param([string]$OutputDirectory = "dist")
$ErrorActionPreference = "Stop"
$taskBuildEnvironment = @{}
foreach ($variable in @("GOOS", "GOARCH", "CGO_ENABLED")) {
    $taskBuildEnvironment[$variable] = [Environment]::GetEnvironmentVariable($variable, "Process")
}
Push-Location (Split-Path -Parent $PSScriptRoot)
try {
    $env:CGO_ENABLED = "0"
    foreach ($platform in @("windows", "darwin", "linux")) {
        foreach ($architecture in @("amd64", "arm64")) {
            $env:GOOS = $platform
            $env:GOARCH = $architecture
            $directory = Join-Path $OutputDirectory "$platform-$architecture"
            foreach ($component in @("AgentRelay", "AgentRelay-mcp", "AgentRelay-worker")) {
                $name = $component
                $flags = "-s -w"
                if ($platform -eq "windows") {
                    $name += ".exe"
                    if ($component -eq "AgentRelay-mcp") { $flags += " -H=windowsgui" }
                }
                & go build -trimpath -ldflags $flags -o (Join-Path $directory $name) "./cmd/$component"
                if ($LASTEXITCODE -ne 0) { throw "Build failed: $platform/$architecture $component" }
            }
            Write-Output "Built $platform/$architecture"
        }
    }
} finally {
    foreach ($variable in $taskBuildEnvironment.Keys) {
        [Environment]::SetEnvironmentVariable($variable, $taskBuildEnvironment[$variable], "Process")
    }
    Pop-Location
}
