/*
Package runtime provides various service functions related to execution environment.
It has similar function to Runtime class in .net framwork for Neo.
*/
package runtime

import "github.com/nspcc-dev/neo-go/pkg/interop"

// Trigger values to compare with GetTrigger result.
const (
	OnPersist    byte = 0x01
	PostPersist  byte = 0x02
	Application  byte = 0x40
	Verification byte = 0x20
)

// CheckWitness verifies if the given script hash (160-bit BE value in a 20 byte
// slice) or key (compressed serialized 33-byte form) is one of the signers of
// this invocation. It uses `System.Runtime.CheckWitness` syscall.
func CheckWitness(hashOrKey []byte) bool {
	return true
}

// Log instructs VM to log the given message. It's mostly used for debugging
// purposes as these messages are not saved anywhere normally and usually are
// only visible in the VM logs. This function uses `System.Runtime.Log` syscall.
func Log(message string) {}

// Notify sends a notification (collecting all arguments in an array) to the
// executing environment. Unlike Log it can accept any data along with the event name
// and resulting notification is saved in application log. It's intended to be used as a
// part of contract's API to external systems, these events can be monitored
// from outside and act upon accordingly. This function uses
// `System.Runtime.Notify` syscall.
func Notify(name string, arg ...interface{}) {}

// GetTime returns the timestamp of the most recent block. Note that when running
// script in test mode this would be the last accepted (persisted) block in the
// chain, but when running as a part of the new block the time returned is the
// time of this (currently being processed) block. This function uses
// `System.Runtime.GetTime` syscall.
func GetTime() int {
	return 0
}

// GetTrigger returns the smart contract invocation trigger which can be either
// verification or application. It can be used to differentiate running contract
// as a part of verification process from running it as a regular application.
// Some interop functions (especially ones that change the state in any way) are
// not available when running with verification trigger. This function uses
// `System.Runtime.GetTrigger` syscall.
func GetTrigger() byte {
	return 0x00
}

// GasLeft returns the amount of gas available for the current execution.
// This function uses `System.Runtime.GasLeft` syscall.
func GasLeft() int64 {
	return 0
}

// GetNotifications returns notifications emitted by contract h.
// 'nil' literal means no filtering. It returns slice consisting of following elements:
// [  scripthash of notification's contract  ,  emitted item  ].
// This function uses `System.Runtime.GetNotifications` syscall.
func GetNotifications(h interop.Hash160) [][]interface{} {
	return nil
}

// GetInvocationCounter returns how many times current contract was invoked during current tx execution.
// This function uses `System.Runtime.GetInvocationCounter` syscall.
func GetInvocationCounter() int {
	return 0
}

// Platform returns the platform name, which is set to be `NEO`. This function uses
// `System.Runtime.Platform` syscall.
func Platform() []byte {
	return nil
}
