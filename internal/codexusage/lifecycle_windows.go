package codexusage

import (
	"errors"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type serverLifecycle struct {
	command *exec.Cmd
	job     windows.Handle
}

func newServerLifecycle(command *exec.Cmd) (*serverLifecycle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	// No wrapper code can run or spawn a child before job assignment succeeds.
	command.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	return &serverLifecycle{command: command, job: job}, nil
}

func (lifecycle *serverLifecycle) started() error {
	pid := uint32(lifecycle.command.Process.Pid)
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(process) }()
	if err := windows.AssignProcessToJobObject(lifecycle.job, process); err != nil {
		return err
	}
	// exec.Cmd does not expose the primary thread handle. Locate only the
	// suspended process's thread; this is not process-tree discovery/containment.
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	var threadID uint32
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID == pid {
			if threadID != 0 {
				return errors.New("suspended server thread is ambiguous")
			}
			threadID = entry.ThreadID
		}
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) || threadID == 0 {
		return errors.New("suspended server thread is unavailable")
	}
	thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, threadID)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(thread) }()
	_, err = windows.ResumeThread(thread)
	return err
}

func (lifecycle *serverLifecycle) terminate() error {
	err := windows.TerminateJobObject(lifecycle.job, 1)
	// Also cover a suspended root whose job assignment failed. It cannot have
	// executed code or spawned descendants in that case.
	if lifecycle.command.Process != nil {
		_ = lifecycle.command.Process.Kill()
	}
	return err
}

func (lifecycle *serverLifecycle) empty() (bool, error) {
	var accounting struct {
		TotalUserTime, TotalKernelTime, PeriodUserTime, PeriodKernelTime int64
		PageFaults, TotalProcesses, ActiveProcesses, TerminatedProcesses uint32
	}
	err := windows.QueryInformationJobObject(lifecycle.job, windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil)
	return accounting.ActiveProcesses == 0, err
}

func (lifecycle *serverLifecycle) close() error { return windows.CloseHandle(lifecycle.job) }
