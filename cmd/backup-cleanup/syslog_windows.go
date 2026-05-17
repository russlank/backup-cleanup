//go:build windows

package main

// writeSyslog is a no-op on Windows because the syslog daemon is not available.
//
// <summary>
// Platform stub for syslog on Windows. All log output goes to stdout/stderr.
// </summary>
func writeSyslog(tag string, level string, msg string) {}
