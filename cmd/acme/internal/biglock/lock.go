package biglock

import (
	"fmt"
	"log"
	"runtime"
	"sync"
	"time"
)

var big sync.Mutex
var stk = make([]byte, 1<<20)

var heldLock LockInfo
var lockID int

type LockInfo struct {
	id    int
	goid  int64
	about string
}

func init() {
	go func() {
		for {
			time.Sleep(10 * time.Second)
			locked := make(chan struct{})
			go func() {
				big.Lock()
				big.Unlock()
				locked <- struct{}{}
			}()
			select {
			case <-locked:
			case <-time.After(20 * time.Second):
				log.Printf("probable deadlock on bigLock; currently held by goroutine %d; %s", heldLock.goid, heldLock.about)
			}
		}
	}()
}

func Lock(about string) LockInfo {
	big.Lock()
	lockID++
	heldLock = LockInfo{
		id:    lockID,
		goid:  runtime.GoroutineID(),
		about: about,
	}
	return heldLock
}

func Unlock(info LockInfo) {
	if info.id != 0 && info.id != heldLock.id {
		panic(fmt.Errorf("unlocking with wrong lock id (%v); currently held by goroutine %d; %s", info.about, heldLock.goid, heldLock.about))
	}
	heldLock = LockInfo{}
	big.Unlock()
}
