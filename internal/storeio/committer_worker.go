package storeio

import (
	"os"
	"runtime"
	"slices"
)

func (c *Committer) stopAccepting() {
	c.closing.Store(true)
	for c.publishers.Load() != 0 {
		runtime.Gosched()
	}
}

func (c *Committer) run(file *os.File, initialized chan<- committerInit, open deviceOpener) {
	locked := c.deviceOptions.Backend != BackendPortable
	if locked {
		runtime.LockOSThread()
		c.deviceOptions.SingleIssuer = true
	}
	device, err := open(file, c.deviceOptions)
	if err != nil {
		if locked {
			runtime.UnlockOSThread()
		}
		c.setFailure(err)
		initialized <- committerInit{err: err}
		close(c.done)
		c.broadcast()
		return
	}
	if locked && device.Backend() != BackendIOUring {
		runtime.UnlockOSThread()
		locked = false
	}
	if locked {
		defer runtime.UnlockOSThread()
	}
	c.device = device
	c.backend = device.Backend()
	c.buffers = make([][]byte, c.bufferCount)
	for i := range c.buffers {
		buffer, bufferErr := device.Buffer(i)
		if bufferErr != nil {
			_ = device.Close()
			c.setFailure(bufferErr)
			initialized <- committerInit{err: bufferErr}
			close(c.done)
			c.broadcast()
			return
		}
		c.buffers[i] = buffer
	}
	initialized <- committerInit{}
	defer func() {
		if closeErr := device.Close(); closeErr != nil {
			c.setFailure(closeErr)
		}
		close(c.done)
		c.broadcast()
	}()

	for {
		batch, ok := c.nextBatch(true)
		if !ok {
			return
		}
		c.groupScratch = c.groupScratch[:1]
		c.groupScratch[0] = batch
		c.commitScratch = c.commitScratch[:len(batch.pages)]
		copy(c.commitScratch, batch.pages)
		latest := batch
		for len(c.groupScratch) < c.options.GroupLimit {
			next, exists := c.peekBatch()
			if !exists || len(c.commitScratch)+len(next.pages) > cap(c.commitScratch) {
				break
			}
			next, _ = c.nextBatch(false)
			groupIndex := len(c.groupScratch)
			c.groupScratch = c.groupScratch[:groupIndex+1]
			c.groupScratch[groupIndex] = next
			writeIndex := len(c.commitScratch)
			c.commitScratch = c.commitScratch[:writeIndex+len(next.pages)]
			copy(c.commitScratch[writeIndex:], next.pages)
			latest = next
		}
		slices.SortFunc(c.commitScratch, func(a, b Write) int {
			if a.Offset < b.Offset {
				return -1
			}
			if a.Offset > b.Offset {
				return 1
			}
			return 0
		})
		if err := device.Commit(c.commitScratch, latest.root); err != nil {
			c.setFailure(err)
			for _, grouped := range c.groupScratch {
				c.release(grouped)
			}
			c.drainFailed()
			return
		}
		groupSize := uint32(len(c.groupScratch))
		c.deviceCommits.Add(1)
		c.batchesDone.Add(uint64(groupSize))
		for old := c.largestGroup.Load(); groupSize > old && !c.largestGroup.CompareAndSwap(old, groupSize); old = c.largestGroup.Load() {
		}
		c.durable.Store(latest.generation)
		c.broadcast()
		for _, grouped := range c.groupScratch {
			c.release(grouped)
		}
	}
}

func (c *Committer) nextBatch(wait bool) (*Batch, bool) {
	for {
		head := c.head.Load()
		if head != c.tail.Load() {
			batch := c.pending[head&c.pendingMask]
			c.pending[head&c.pendingMask] = nil
			c.head.Store(head + 1)
			return batch, true
		}
		if !wait {
			return nil, false
		}
		c.workerWait.Store(1)
		if c.head.Load() != c.tail.Load() {
			c.workerWait.Store(0)
			continue
		}
		select {
		case <-c.wake:
		case <-c.stop:
			c.workerWait.Store(0)
			if c.head.Load() == c.tail.Load() {
				return nil, false
			}
			continue
		}
		c.workerWait.Store(0)
	}
}

func (c *Committer) peekBatch() (*Batch, bool) {
	head := c.head.Load()
	if head == c.tail.Load() {
		return nil, false
	}
	return c.pending[head&c.pendingMask], true
}

func (c *Committer) drainFailed() {
	for {
		batch, ok := c.nextBatch(false)
		if !ok {
			return
		}
		c.release(batch)
	}
}

func (c *Committer) setFailure(err error) {
	if err == nil {
		return
	}
	c.failOnce.Do(func() {
		c.failure.Store(&commitFailure{err: err})
		c.stopAccepting()
		close(c.failed)
		c.broadcast()
	})
}

func (c *Committer) release(batch *Batch) {
	for _, write := range batch.pages {
		c.freeBuffers.push(uint32(write.Buffer))
	}
	c.freeBuffers.push(uint32(batch.root.Buffer))
	batch.pages = batch.pages[:0]
	batch.root = Write{}
	batch.generation = 0
	batch.state.Store(batchFree)
	c.freeBatches.push(batch.index)
}

func (c *Committer) releasePartial(batch *Batch, acquired int) {
	pageCount := len(batch.pages)
	for i := 0; i < acquired; i++ {
		if i == pageCount {
			c.freeBuffers.push(uint32(batch.root.Buffer))
		} else {
			c.freeBuffers.push(uint32(batch.pages[i].Buffer))
		}
	}
	batch.pages = batch.pages[:0]
	batch.root = Write{}
	batch.state.Store(batchFree)
	c.freeBatches.push(batch.index)
}

func (c *Committer) broadcast() {
	c.waitMu.Lock()
	c.wait.Broadcast()
	c.waitMu.Unlock()
}
