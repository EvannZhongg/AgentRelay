# Windows PowerShell 5.1 executor, sent through the SSH channel, never installed.
$ErrorActionPreference = 'Stop'
$utf8 = New-Object System.Text.UTF8Encoding($false)
# Input encoding is set once in the bootstrap. Setting it again would replace
# Console.In and discard buffered bytes from the next (request) line.
[Console]::OutputEncoding = $utf8
function Write-Field([string]$key, [byte[]]$data) { [Console]::WriteLine($key + "`t" + [Convert]::ToBase64String($data)) }
function Resolve-Target([string]$root, [string]$relative) {
    if ([IO.Path]::IsPathRooted($relative) -or $relative.Contains(':')) { throw 'path must be workspace-relative' }
    $target = [IO.Path]::GetFullPath([IO.Path]::Combine($root, $relative))
    $prefix = $root.TrimEnd('\') + '\'
    if ($target -ne $root -and -not $target.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) { throw 'path escapes authorized workspace' }
    # Reject child symlinks/junctions, including those in intermediate segments.
    $current = $root
    foreach ($part in $relative.Replace('/', '\').Split('\')) {
        if (!$part -or $part -eq '.') { continue }
        $current = [IO.Path]::Combine($current, $part)
        if (([IO.File]::GetAttributes($current) -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw 'reparse-point paths are not allowed' }
    }
    return $target
}
try {
    $request = [Console]::In.ReadLine() | ConvertFrom-Json
    $root = [IO.Path]::GetFullPath([string]$request.workspace)
    if (![IO.Directory]::Exists($root)) { throw 'authorized workspace must exist' }
    if ($request.operation -eq 'probe') {
        [Console]::WriteLine("AgentRelaySSH1`nstatus`t200")
        Write-Field 'os' ($utf8.GetBytes('windows'))
        $architecture = [string]$env:PROCESSOR_ARCHITECTURE
        if ($env:PROCESSOR_ARCHITEW6432) { $architecture = $env:PROCESSOR_ARCHITEW6432 }
        switch ($architecture.ToUpperInvariant()) { 'AMD64' {$architecture='amd64'} 'ARM64' {$architecture='arm64'} 'X86' {$architecture='386'} }
        Write-Field 'architecture' ($utf8.GetBytes($architecture))
        return
    }
    $target = Resolve-Target $root ([string]$request.path)
    if ($request.operation -eq 'read') {
        $stream = [IO.File]::OpenRead($target)
        try {
            if ($stream.Length -gt 16777216) { throw 'file read exceeds 16 MiB' }
            $data = New-Object byte[] ([int]$stream.Length)
            $offset = 0
            while ($offset -lt $data.Length) {
                $count = $stream.Read($data, $offset, $data.Length-$offset)
                if ($count -eq 0) { throw 'file changed while reading' }
                $offset += $count
            }
        } finally { $stream.Dispose() }
        [Console]::WriteLine("AgentRelaySSH1`nstatus`t200")
        Write-Field 'content' $data
        return
    }
    if ($request.operation -ne 'exec') { throw 'unsupported SSH operation' }
    if (![IO.Directory]::Exists($target)) { throw 'working directory does not exist' }
    # C# uses only Windows PowerShell's bundled .NET Framework. Concurrent
    # collectors drain native byte streams; a Job Object owns the process tree.
    Add-Type -TypeDefinition @"
using System;
using System.IO;
using System.Diagnostics;
using System.Runtime.InteropServices;
using System.Threading;
public sealed class RelaySSHJob : IDisposable {
 [StructLayout(LayoutKind.Sequential)] struct Basic { public long User,Job; public uint Flags; public UIntPtr Min,Max; public uint Active; public UIntPtr Affinity; public uint Priority,Scheduling; }
 [StructLayout(LayoutKind.Sequential)] struct Counters { public ulong A,B,C,D,E,F; }
 [StructLayout(LayoutKind.Sequential)] struct Extended { public Basic Basic; public Counters IO; public UIntPtr ProcessMemory,JobMemory,PeakProcess,PeakJob; }
 [DllImport("kernel32.dll",SetLastError=true)] static extern IntPtr CreateJobObject(IntPtr attributes,string name);
 [DllImport("kernel32.dll",SetLastError=true)] static extern bool SetInformationJobObject(IntPtr job,int kind,IntPtr data,uint size);
 [DllImport("kernel32.dll",SetLastError=true)] static extern bool AssignProcessToJobObject(IntPtr job,IntPtr process);
 [DllImport("kernel32.dll")] static extern bool CloseHandle(IntPtr handle);
 IntPtr handle;
 readonly object gate = new object();
 public RelaySSHJob() {
  handle=CreateJobObject(IntPtr.Zero,null);
  if(handle==IntPtr.Zero) throw new System.ComponentModel.Win32Exception();
  Extended info=new Extended(); info.Basic.Flags=0x2000;
  int size=Marshal.SizeOf(info); IntPtr memory=Marshal.AllocHGlobal(size);
  try { Marshal.StructureToPtr(info,memory,false); if(!SetInformationJobObject(handle,9,memory,(uint)size)) throw new System.ComponentModel.Win32Exception(); }
  catch { Dispose(); throw; } finally { Marshal.FreeHGlobal(memory); }
 }
 public void Assign(Process process) { if(!AssignProcessToJobObject(handle,process.Handle)) throw new System.ComponentModel.Win32Exception(); }
 public void Dispose() { lock(gate) { if(handle!=IntPtr.Zero) { CloseHandle(handle); handle=IntPtr.Zero; } } }
}
public sealed class RelaySSHOutput {
 const int Limit=1048576;
 public byte[] Bytes; public bool Truncated;
 public void Collect(Stream stream) {
  using(MemoryStream data=new MemoryStream()) {
   byte[] buffer=new byte[65536]; int n;
   while((n=stream.Read(buffer,0,buffer.Length))>0) { int keep=Math.Min(n,Limit-(int)data.Length); data.Write(buffer,0,keep); if(keep<n) Truncated=true; }
   Bytes=data.ToArray();
  }
 }
}
public sealed class RelaySSHResult { public byte[] Out,Err; public int Code; public bool TimedOut,Truncated; }
public static class RelaySSHExecute {
 public static RelaySSHResult Run(ProcessStartInfo info,int timeout,string command) {
  using(RelaySSHJob job=new RelaySSHJob()) using(Process p=new Process()) {
   p.StartInfo=info;
   if(!p.Start()) throw new Exception("cannot start native shell");
   try {
    // The child waits for a stdin gate before it runs user code. Assigning
    // its job first removes the race where user code spawns untracked children.
    job.Assign(p);
    RelaySSHOutput stdout=new RelaySSHOutput(), stderr=new RelaySSHOutput();
    Thread outThread=new Thread(delegate(){stdout.Collect(p.StandardOutput.BaseStream);});
    Thread errThread=new Thread(delegate(){stderr.Collect(p.StandardError.BaseStream);});
    outThread.IsBackground=true;errThread.IsBackground=true;outThread.Start();errThread.Start();
    int interrupted=0;
    Thread disconnected=new Thread(delegate(){ try { while(Console.In.Read()!=-1){} Interlocked.Exchange(ref interrupted,1);job.Dispose(); } catch(IOException){Interlocked.Exchange(ref interrupted,1);job.Dispose();} });
    disconnected.IsBackground=true;disconnected.Start();
    p.StandardInput.WriteLine(Convert.ToBase64String(System.Text.Encoding.UTF8.GetBytes(command)));p.StandardInput.Close();
    bool timedOut=!p.WaitForExit(timeout);
    // Closing the job also stops children retaining stdout pipes after exit.
    job.Dispose();p.WaitForExit();outThread.Join();errThread.Join();
    return new RelaySSHResult {Out=stdout.Bytes,Err=stderr.Bytes,Code=timedOut?124:(interrupted==1?125:p.ExitCode),TimedOut=timedOut,Truncated=stdout.Truncated||stderr.Truncated};
   } catch { job.Dispose(); if(!p.HasExited) p.Kill(); throw; }
  }
 }
}
"@
    $command = [string]$request.exec.command
    # Only a small bootstrap is encoded on the command line. The user's command
    # is sent as a base64 stdin line after job assignment, including large input.
    $child = '$ProgressPreference="SilentlyContinue";[Console]::InputEncoding=New-Object System.Text.UTF8Encoding($false);[Console]::OutputEncoding=New-Object System.Text.UTF8Encoding($false);$OutputEncoding=[Console]::OutputEncoding;$command=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadLine()));$global:LASTEXITCODE=0;try { & ([ScriptBlock]::Create($command));$ok=$?;if($global:LASTEXITCODE -ne 0){exit $global:LASTEXITCODE};if(-not $ok){exit 1};exit 0 } catch { [Console]::Error.WriteLine($_);exit 1 }'

    $info = New-Object Diagnostics.ProcessStartInfo
    $info.FileName = Join-Path $PSHOME 'powershell.exe'
    $info.Arguments = '-NoLogo -NoProfile -NonInteractive -OutputFormat Text -EncodedCommand ' + [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($child))
    $info.WorkingDirectory = $target
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $info.RedirectStandardInput = $true
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    if ($request.exec.env) { foreach ($property in $request.exec.env.PSObject.Properties) { $info.EnvironmentVariables[$property.Name] = [string]$property.Value } }
    $result = [RelaySSHExecute]::Run($info, [int]$request.exec.timeout_ms, $command)
    [Console]::WriteLine("AgentRelaySSH1`nstatus`t200`nexit_code`t" + $result.Code + "`ntimed_out`t" + [int]$result.TimedOut + "`ntruncated`t" + [int]$result.Truncated)
    Write-Field 'stdout' $result.Out
    Write-Field 'stderr' $result.Err
} catch {
    $status = 400
    if ($_.Exception -is [IO.FileNotFoundException] -or $_.Exception -is [IO.DirectoryNotFoundException]) { $status = 404 }
    [Console]::WriteLine("AgentRelaySSH1`nstatus`t" + $status)
    Write-Field 'error' ($utf8.GetBytes([string]$_))
}
