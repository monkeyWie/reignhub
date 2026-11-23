package system

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"github.com/monkeyWie/reignhub/internal/provider"
	"golang.org/x/sys/windows"
)

var errProcessExited = errors.New("process is no longer running")

// ProcessStamp uses one process handle for both fields, so a recycled PID
// cannot combine the executable of one generation with another's start time.
func ProcessStamp(ctx context.Context, pid int) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if pid <= 0 {
		return "", "", fmt.Errorf("invalid process ID")
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", "", err
	}
	defer windows.CloseHandle(h)
	return processStamp(h)
}

func processStamp(h windows.Handle) (string, string, error) {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return "", "", err
	}
	if exited.HighDateTime != 0 || exited.LowDateTime != 0 {
		return "", "", errProcessExited
	}
	buf := make([]uint16, 32768)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return "", "", processProbeError(h, err)
	}
	stamp := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	return strconv.FormatUint(stamp, 10), windows.UTF16ToString(buf[:size]), nil
}

func processProbeError(handle windows.Handle, err error) error {
	const stillActive = 259
	var code uint32
	if windows.GetExitCodeProcess(handle, &code) == nil && code != stillActive {
		return errProcessExited
	}
	return err
}

// Discover is passive and matches full executable paths. Electron subprocesses
// share the main executable, so only roots of that executable's process tree
// count as desktop instances; CLI/app-server processes never count as the App.
func Discover(ctx context.Context, app string) ([]provider.Instance, error) {
	instances := []provider.Instance{}
	executable := AppExecutable(app)
	if executable == "" {
		return instances, nil
	}
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil {
		return instances, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return instances, err
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return instances, err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	parents := map[int]int{}
	candidates := []provider.Instance{}
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if err := ctx.Err(); err != nil {
			return instances, err
		}
		pid := int(entry.ProcessID)
		parents[pid] = int(entry.ParentProcessID)
		if !strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), filepath.Base(executable)) {
			continue
		}
		var processSession uint32
		if windows.ProcessIdToSessionId(entry.ProcessID, &processSession) != nil || processSession != session {
			continue
		}
		started, path, e := desktopProcess(pid, executable, user.User.Sid)
		if e != nil {
			// A process may exit between the snapshot and opening its handle.
			if errors.Is(e, windows.ERROR_INVALID_PARAMETER) || errors.Is(e, errProcessExited) {
				continue
			}
			return instances, fmt.Errorf("读取 Codex App 进程 %d 失败：%w", pid, e)
		}
		if path != "" {
			candidates = append(candidates, provider.Instance{PID: pid, StartedAt: started, Path: filepath.Clean(app)})
		}
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return instances, err
	}
	return desktopRoots(candidates, parents), nil
}

func desktopProcess(pid int, executable string, sid *windows.SID) (string, string, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", "", err
	}
	defer windows.CloseHandle(handle)
	started, path, err := processStamp(handle)
	if err != nil || !strings.EqualFold(filepath.Clean(path), executable) {
		return "", "", err
	}
	var token windows.Token
	if err := windows.OpenProcessToken(handle, windows.TOKEN_QUERY, &token); err != nil {
		return "", "", processProbeError(handle, err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil || !sid.Equals(user.User.Sid) {
		return "", "", err
	}
	helper, err := electronHelper(handle)
	if err != nil {
		return "", "", processProbeError(handle, err)
	}
	if helper {
		return "", "", nil
	}
	return started, path, nil
}

// Electron subprocesses can outlive their parent. Their own Chromium process
// type distinguishes them even after the parent is missing from the snapshot.
func electronHelper(handle windows.Handle) (bool, error) {
	// Windows command lines are bounded to 32,767 UTF-16 code units.
	var buffer struct {
		command windows.NTUnicodeString
		text    [32768]uint16
	}
	var size uint32
	if err := windows.NtQueryInformationProcess(handle, windows.ProcessCommandLineInformation, unsafe.Pointer(&buffer), uint32(unsafe.Sizeof(buffer)), &size); err != nil {
		return false, err
	}
	command := buffer.command
	if command.Buffer != &buffer.text[0] || command.Length == 0 || command.Length%2 != 0 {
		return false, errors.New("Windows 未返回进程启动参数")
	}
	args, err := windows.DecomposeCommandLine(windows.UTF16ToString(buffer.text[:int(command.Length)/2]))
	if err != nil {
		return false, err
	}
	for _, arg := range args {
		// argv[0] is the full executable path.
		if arg == "--" {
			break
		}
		if strings.HasPrefix(arg, "--type=") || arg == "--type" {
			return true, nil
		}
	}
	return false, nil
}

func desktopRoots(candidates []provider.Instance, parents map[int]int) []provider.Instance {
	matched := map[int]bool{}
	for _, p := range candidates {
		matched[p.PID] = true
	}
	instances := []provider.Instance{}
	for _, p := range candidates {
		seen := map[int]bool{p.PID: true}
		child := false
		for parent := parents[p.PID]; parent != 0 && !seen[parent]; parent = parents[parent] {
			if matched[parent] {
				child = true
				break
			}
			seen[parent] = true
		}
		if !child {
			instances = append(instances, p)
		}
	}
	return instances
}
