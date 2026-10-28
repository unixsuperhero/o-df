package main

import (
	"syscall"
	"time"
)

// stamp is the newer of a file's access and change times.
func stamp(st *syscall.Stat_t) time.Time {
	return newer(time.Unix(st.Atimespec.Sec, st.Atimespec.Nsec), time.Unix(st.Mtimespec.Sec, st.Mtimespec.Nsec))
}
