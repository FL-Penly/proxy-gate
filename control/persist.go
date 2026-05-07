package control

import (
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/codeking-ai/cligate-v2/store"
	"go.etcd.io/bbolt"
)

type Op struct {
	Bucket string
	Key    string
	Value  []byte
	Delete bool
}

type Queue struct {
	store    *store.Store
	ops      chan Op
	wg       sync.WaitGroup
	closeMu  sync.Mutex
	closed   bool
	closeCh  chan struct{}
	flushReq chan chan struct{}

	batchSize int
	maxWait   time.Duration
}

type QueueOption func(*Queue)

func WithBatchSize(n int) QueueOption {
	return func(q *Queue) { q.batchSize = n }
}

func WithMaxWait(d time.Duration) QueueOption {
	return func(q *Queue) { q.maxWait = d }
}

func NewQueue(s *store.Store, opts ...QueueOption) *Queue {
	q := &Queue{
		store:     s,
		ops:       make(chan Op, 1024),
		closeCh:   make(chan struct{}),
		flushReq:  make(chan chan struct{}, 4),
		batchSize: 64,
		maxWait:   100 * time.Millisecond,
	}
	for _, o := range opts {
		o(q)
	}
	q.wg.Add(1)
	go q.loop()
	return q
}

var ErrQueueClosed = errors.New("persist: queue closed")

func (q *Queue) Put(bucket, key string, value []byte) error {
	return q.send(Op{Bucket: bucket, Key: key, Value: value})
}

func (q *Queue) Delete(bucket, key string) error {
	return q.send(Op{Bucket: bucket, Key: key, Delete: true})
}

func (q *Queue) send(op Op) error {
	q.closeMu.Lock()
	if q.closed {
		q.closeMu.Unlock()
		return ErrQueueClosed
	}
	q.closeMu.Unlock()
	select {
	case q.ops <- op:
		return nil
	case <-q.closeCh:
		return ErrQueueClosed
	}
}

func (q *Queue) Flush() {
	ack := make(chan struct{})
	select {
	case q.flushReq <- ack:
		<-ack
	case <-q.closeCh:
	}
}

func (q *Queue) Close() {
	q.closeMu.Lock()
	if q.closed {
		q.closeMu.Unlock()
		return
	}
	q.closed = true
	close(q.ops)
	close(q.closeCh)
	q.closeMu.Unlock()
	q.wg.Wait()
}

func (q *Queue) loop() {
	defer q.wg.Done()
	batch := make([]Op, 0, q.batchSize)
	timer := time.NewTimer(q.maxWait)
	timer.Stop()
	timerActive := false

	flushBatch := func() {
		if len(batch) == 0 {
			return
		}
		if err := q.commit(batch); err != nil {
			slog.Error("persist: commit failed", "err", err, "ops", len(batch))
		}
		batch = batch[:0]
	}

	for {
		select {
		case op, ok := <-q.ops:
			if !ok {
				flushBatch()
				if timerActive {
					timer.Stop()
				}
				for {
					select {
					case ack := <-q.flushReq:
						close(ack)
					default:
						return
					}
				}
			}
			batch = append(batch, op)
			if !timerActive {
				timer.Reset(q.maxWait)
				timerActive = true
			}
			if len(batch) >= q.batchSize {
				flushBatch()
				timer.Stop()
				timerActive = false
			}
		case <-timer.C:
			timerActive = false
			flushBatch()
		case ack := <-q.flushReq:
			drainOps := true
			for drainOps {
				select {
				case op, ok := <-q.ops:
					if !ok {
						drainOps = false
						break
					}
					batch = append(batch, op)
				default:
					drainOps = false
				}
			}
			flushBatch()
			if timerActive {
				timer.Stop()
				timerActive = false
			}
			close(ack)
		}
	}
}

func (q *Queue) commit(batch []Op) error {
	return q.store.DB().Update(func(tx *bbolt.Tx) error {
		for _, op := range batch {
			b := tx.Bucket([]byte(op.Bucket))
			if b == nil {
				slog.Warn("persist: missing bucket", "bucket", op.Bucket)
				continue
			}
			if op.Delete {
				if err := b.Delete([]byte(op.Key)); err != nil {
					return err
				}
				continue
			}
			if err := b.Put([]byte(op.Key), op.Value); err != nil {
				return err
			}
		}
		return nil
	})
}
