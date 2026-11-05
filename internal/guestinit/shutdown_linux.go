package guestinit

import "syscall"

func shutdown() { syscall.Sync(); _ = syscall.Reboot(syscall.LINUX_REBOOT_CMD_RESTART) }
