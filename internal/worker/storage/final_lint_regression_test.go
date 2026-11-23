package storage

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFinalLintS3ErrorDrainPreservesStatusAndObservableCause(t *testing.T) {
	for _, name := range []string{"read", "close", "cancel_before", "cancel_during"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			body := newS3BudgetBody(256 << 10)
			cause := errors.New("构造的有界排空失败")
			switch name {
			case "read":
				body.readErr = cause
			case "close":
				body.closeErr = cause
			case "cancel_before":
				cause = context.Canceled
				cancel()
			case "cancel_during":
				cause = context.Canceled
				body.cancelOnRead = cancel
			}
			store, transport := newS3BudgetStore(t, http.StatusServiceUnavailable, body)
			reader, err := store.Download(ctx, "constructed-final-lint-object")
			require.Nil(t, reader)
			require.Error(t, err)
			require.ErrorIs(t, err, cause, "排空异常不能被伪装为成功或无法核实的状态错误")
			if transport.called.Load() {
				require.EqualError(t, err, "S3 下载失败: HTTP 503", "主要错误仍是原HTTP状态，错误正文不得输出")
				require.Equal(t, int64(1), body.closes.Load())
				require.LessOrEqual(t, body.readBytes.Load(), s3DownloadDrainLimitForTest)
			}
		})
	}
}

func TestFinalLintS3DrainDataAndErrorRemainBounded(t *testing.T) {
	cause := errors.New("构造的尾部读错误")
	body := &finalLintS3PartialErrorBody{cause: cause}
	store, _ := newS3BudgetStore(t, http.StatusForbidden, body)
	reader, err := store.Download(context.Background(), "constructed-partial-error")
	require.Nil(t, reader)
	require.EqualError(t, err, "S3 下载失败: HTTP 403")
	require.ErrorIs(t, err, cause)
	require.Equal(t, 1, body.reads)
	require.Equal(t, 1, body.closes)
}

type finalLintS3PartialErrorBody struct {
	cause  error
	reads  int
	closes int
}

func (b *finalLintS3PartialErrorBody) Read(dst []byte) (int, error) {
	b.reads++
	if b.reads != 1 {
		return 0, io.EOF
	}
	return copy(dst, "partial"), b.cause
}

func (b *finalLintS3PartialErrorBody) Close() error {
	b.closes++
	return nil
}
