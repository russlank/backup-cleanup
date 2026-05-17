//go:build !windows

package main

import "log/syslog"

// writeSyslog writes msg to the system log on Unix-like platforms.
//
// <summary>
// Best-effort syslog integration using the standard log/syslog package.
// </summary>
//
// <remarks>
// The Bash script used `logger ... 2>/dev/null || true`, so syslog failures
// must not fail the cleanup job.
// </remarks>
func writeSyslog(tag string, level string, msg string) {
	var priority syslog.Priority
	switch level {
	case "error":
		priority = syslog.LOG_USER | syslog.LOG_ERR
	case "warning":
		priority = syslog.LOG_USER | syslog.LOG_WARNING
	default:
		priority = syslog.LOG_USER | syslog.LOG_INFO
	}
	writer, err := syslog.New(priority, tag)
	if err != nil {
		return
	}
	defer writer.Close()
	_, _ = writer.Write([]byte(msg))
}
