package main

import (
	"os/exec"
	"syscall"
)

// GUI 程式呼叫主控台程式（gswin64c）時不要彈出黑色視窗
func hideWindow(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
