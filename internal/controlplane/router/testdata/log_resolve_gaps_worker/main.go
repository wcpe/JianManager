package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/grpc/metadata"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/query/grpcsvc"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 本夹具只经本地标准输入输出使用真实 Worker 服务与账本，不开放端口或连接外部服务。
// 独立进程隔离 CP 与 Worker 的 SQLite 驱动注册，不能把它替换为伪造裁定成功的桩。
type binding struct {
	UUID       string `json:"uuid"`
	Target     string `json:"target"`
	Generation string `json:"generation"`
	WorkDir    string `json:"workDir"`
}

type request struct {
	Action   string                                `json:"action"`
	Root     string                                `json:"root,omitempty"`
	Sources  []binding                             `json:"sources,omitempty"`
	Seed     string                                `json:"seed,omitempty"`
	RPC      *workerpb.LogResolveIngestGapsRequest `json:"rpc,omitempty"`
	Metadata map[string][]string                   `json:"metadata,omitempty"`
}

type response struct {
	Error           string                        `json:"error,omitempty"`
	RPC             *workerpb.LogTaskResponse     `json:"rpc,omitempty"`
	Ranges          []ingest.AbandonedSourceRange `json:"ranges"`
	Gaps            []stateindex.GapRow           `json:"gaps"`
	ConsumedSources int                           `json:"consumedSources"`
}

type fixture struct {
	root    string
	cat     *catalog.Catalog
	manager *ingest.Manager
}

func (f *fixture) reopen() error {
	manager, err := ingest.New(ingest.Options{Root: f.root, Catalog: f.cat, Journal: f.cat.Journal()})
	if err == nil {
		f.manager = manager
	}
	return err
}

func (f *fixture) init(req request) error {
	f.root, f.cat = req.Root, catalog.New(nil)
	if err := f.reopen(); err != nil {
		return err
	}
	for _, source := range req.Sources {
		if err := f.manager.RegisterInstance(source.UUID, source.Target, source.Generation, "STDIO_PRIMARY", source.WorkDir); err != nil {
			return err
		}
		if req.Seed == "raw" {
			identity := sha256.Sum256([]byte(source.UUID + "\x00" + source.Generation))
			path := filepath.Join(f.root, "var", "log", "raw", fmt.Sprintf("%x", identity), "stderr.log")
			if err := os.Remove(path); err != nil {
				return err
			}
			if err := f.manager.AppendInstanceOutput(source.UUID, "stderr", []byte("fixture failed output\n")); err == nil {
				return fmt.Errorf("夹具未能产生托管 Raw 写失败")
			}
		} else {
			if err := f.manager.AppendInstanceOutput(source.UUID, "stderr", []byte("0123456789\n")); err != nil {
				return err
			}
			// 暂存并还原本地 Raw，只为让真实 Manager 落下账本根行；下方种子会替换缺口原因。
			identity := sha256.Sum256([]byte(source.UUID + "\x00" + source.Generation))
			path := filepath.Join(f.root, "var", "log", "raw", fmt.Sprintf("%x", identity), "stderr.log")
			if err := os.Rename(path, path+".fixture"); err != nil {
				return err
			}
			writeErr := f.manager.AppendInstanceOutput(source.UUID, "stderr", []byte("fixture failed output\n"))
			if err := os.Rename(path+".fixture", path); err != nil {
				return err
			}
			if writeErr == nil {
				return fmt.Errorf("夹具未能登记账本根行")
			}
		}
	}
	if req.Seed != "delivery" {
		return nil
	}
	// 在纯临时索引中构造已有消费水位、DELIVER_ERROR、未发布投影，专门覆盖历史空键旁路。
	if err := f.manager.Stop(); err != nil {
		return err
	}
	store, err := stateindex.Open(filepath.Join(f.root, "var", "log", "ingest.index.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	state, err := store.Load()
	if err != nil {
		return err
	}
	state.Gaps = nil
	for i := range state.Positions {
		row := &state.Positions[i]
		if strings.Contains(row.Key, "/stderr/") {
			row.ReadPos, row.DurablePos = 10, 10
			row.AcquirePaused, row.PauseReason = true, "fixture delivery failed"
			state.Gaps = append(state.Gaps, stateindex.GapRow{Key: row.Key, ID: 0, StartPos: 2, EndPos: 8, Reason: "DELIVER_ERROR", Detail: "fixture unpublished projection"})
		}
	}
	if len(state.Gaps) != len(req.Sources) {
		return fmt.Errorf("夹具缺口数量不匹配")
	}
	if _, err := store.Apply(state); err != nil {
		return err
	}
	if err := store.Close(); err != nil {
		return err
	}
	return f.reopen()
}

func (f *fixture) snapshot(out *response) error {
	out.Ranges = f.manager.AbandonedRanges(nil)
	store, err := stateindex.OpenReadOnly(filepath.Join(f.root, "var", "log", "ingest.index.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	state, err := store.Load()
	if err != nil {
		return err
	}
	out.Gaps = state.Gaps
	for _, position := range state.Positions {
		if strings.Contains(position.Key, "/stderr/") && position.ReadPos > 0 && position.DurablePos > 0 {
			out.ConsumedSources++
		}
	}
	return nil
}

func (f *fixture) execute(req request) (response, error) {
	var out response
	switch req.Action {
	case "init":
		if err := f.init(req); err != nil {
			return out, err
		}
	case "rpc":
		service := grpcsvc.NewDisabled()
		service.SetIngestGapResolver(f.manager)
		ctx := metadata.NewIncomingContext(context.Background(), metadata.MD(req.Metadata))
		resp, err := service.LogResolveIngestGaps(ctx, req.RPC)
		if err != nil {
			return out, err
		}
		out.RPC = resp
	case "restart":
		if err := f.manager.Stop(); err != nil {
			return out, err
		}
		if err := f.reopen(); err != nil {
			return out, err
		}
	case "snapshot":
	default:
		return out, fmt.Errorf("未知夹具操作 %q", req.Action)
	}
	if f.manager == nil {
		return out, fmt.Errorf("夹具尚未初始化")
	}
	return out, f.snapshot(&out)
}

func main() {
	f := &fixture{}
	defer func() {
		if f.manager != nil {
			_ = f.manager.Stop()
		}
	}()
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var req request
		if err := decoder.Decode(&req); err != nil {
			if err != io.EOF {
				fmt.Fprintln(os.Stderr, err)
			}
			return
		}
		out, err := f.execute(req)
		if err != nil {
			out.Error = err.Error()
		}
		if err := encoder.Encode(out); err != nil {
			return
		}
	}
}
