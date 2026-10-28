package main

import (
	"syscall"
	"time"
)

// stamp is the newer of a file's access and change times.
func stamp(st *syscall.Stat_t) time.Time {
	return newer(time.Unix(st.Atim.Sec, st.Atim.Nsec), time.Unix(st.Mtim.Sec, st.Mtim.Nsec))
}
