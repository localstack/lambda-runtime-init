// User utilities to create UNIX users and drop root privileges
package utils

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"
	"syscall"

	log "github.com/sirupsen/logrus"
)

// AddUser adds a UNIX user (e.g., sbx_user1051) to the passwd and shadow files if not already present
// The actual default values are based on inspecting the AWS Lambda runtime in us-east-1
// /etc/group is empty and /etc/gshadow is not accessible in AWS
// The home directory does not exist in AWS Lambda
func AddUser(user string, uid int, gid int) {
	// passwd file format: https://www.cyberciti.biz/faq/understanding-etcpasswd-file-format/
	passwdFile := "/etc/passwd"
	passwdEntry := fmt.Sprintf("%[1]s:x:%[2]v:%[3]v::/home/%[1]s:/sbin/nologin", user, uid, gid)
	if !doesFileContainEntry(passwdFile, passwdEntry) {
		addEntry(passwdFile, passwdEntry)
	}
	// shadow file format: https://www.cyberciti.biz/faq/understanding-etcshadow-file/
	shadowFile := "/etc/shadow"
	shadowEntry := fmt.Sprintf("%s:*:18313:0:99999:7:::", user)
	if !doesFileContainEntry(shadowFile, shadowEntry) {
		addEntry(shadowFile, shadowEntry)
	}
}

// doesFileContainEntry returns true if the entry string exists in the given file
func doesFileContainEntry(file string, entry string) bool {
	data, err := os.ReadFile(file)
	if err != nil {
		log.Warnln("Could not read file:", file, err)
		return false
	}
	text := string(data)
	return strings.Contains(text, entry)
}

// addEntry appends an entry string to the given file
func addEntry(file string, entry string) error {
	f, err := os.OpenFile(file,
		os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		log.Errorln("Error opening file:", file, err)
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(entry); err != nil {
		log.Errorln("Error appending entry to file:", file, err)
		return err
	}
	return nil
}

// IsRootUser returns true if the current process is root and false otherwise.
func IsRootUser() bool {
	return os.Getuid() == 0
}

// UserLogger returns a context logger with user fields.
func UserLogger() *log.Entry {
	// Skip user lookup at debug level
	if !log.IsLevelEnabled(log.DebugLevel) {
		return log.WithFields(log.Fields{})
	}
	uid := os.Getuid()
	uidString := strconv.Itoa(uid)
	userObject, err := user.LookupId(uidString)
	if err != nil {
		log.Warnln("Could not look up user by uid:", uid, err)
	}
	return log.WithFields(log.Fields{
		"username": userObject.Username,
		"uid":      uid,
		"euid":     os.Geteuid(),
		"gid":      os.Getgid(),
	})
}

// DropPrivileges switches to another UNIX user by dropping root privileges
// Initially based on https://stackoverflow.com/a/75545491/6875981
func DropPrivileges(userToSwitchTo string) error {
	// Lookup user and group IDs for the user we want to switch to.
	userInfo, err := user.Lookup(userToSwitchTo)
	if err != nil {
		log.Errorln("Error looking up user:", userToSwitchTo, err)
		return err
	}
	// Convert group ID and user ID from string to int.
	gid, err := strconv.Atoi(userInfo.Gid)
	if err != nil {
		log.Errorln("Error converting gid:", userInfo.Gid, err)
		return err
	}
	uid, err := strconv.Atoi(userInfo.Uid)
	if err != nil {
		log.Errorln("Error converting uid:", userInfo.Uid, err)
		return err
	}

	// Limitation: Debugger gets stuck when stepping over these syscalls!
	// No breakpoints beyond this point are hit.
	// Set group ID (real and effective).
	if err = syscall.Setgid(gid); err != nil {
		log.Errorln("Failed to set group ID:", err)
		return err
	}
	// Set user ID (real and effective).
	if err = syscall.Setuid(uid); err != nil {
		log.Errorln("Failed to set user ID:", err)
		return err
	}
	return nil
}
