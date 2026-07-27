package workerpb_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/proto/workerpb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestWorkerProto_FleetContractFrozen(t *testing.T) {
	file := workerpb.File_proto_worker_proto
	service := file.Services().ByName("WorkerService")
	require.NotNil(t, service)

	for _, name := range []protoreflect.Name{
		"GetBotCapacity",
		"GetInstanceResourceSnapshot",
		"ApplyBotBatch",
		"GetBotFleetSnapshot",
		"StreamBotFleetEvents",
		"SignalBotActions",
	} {
		require.NotNilf(t, service.Methods().ByName(name), "缺少冻结 fleet RPC %s", name)
	}

	assertMessageFields(t, file, "BotAssignment", []string{
		"bot_uuid", "instance_uuid", "session_uuid", "generation", "desired_state",
		"config_hash", "name", "host", "port", "username", "version", "auth",
		"cohort_key", "scenario_json", "resume_step_id", "connect_not_before_unix_ms",
		"correlation_seed",
	})
	assertMessageFields(t, file, "BotRuntimeSnapshot", []string{
		"bot_uuid", "session_uuid", "generation", "config_hash", "worker_epoch",
		"worker_epoch_generation", "event_seq", "status", "current_step_id", "health",
		"food", "pos", "reconnect_count", "error_code",
		"last_error", "observed_at_unix_ms",
	})
	assertMessageFields(t, file, "BotActionEvent", []string{
		"bot_uuid", "session_uuid", "generation", "action_run_id", "step_id", "attempt",
		"status", "error_code", "message", "correlation_token", "result_json",
		"duration_ms", "observed_at_unix_ms",
	})

	fleetEvent := file.Messages().ByName("BotFleetEvent")
	require.NotNil(t, fleetEvent)
	require.Equal(t, 1, fleetEvent.Oneofs().Len())
	require.Equal(t, protoreflect.Name("event"), fleetEvent.Oneofs().Get(0).Name())
	eventOneof := fleetEvent.Oneofs().Get(0)
	require.Equal(t, 2, eventOneof.Fields().Len())
	require.Equal(t, protoreflect.Name("runtime_snapshot"), eventOneof.Fields().Get(0).Name())
	require.Equal(t, protoreflect.FieldNumber(1), eventOneof.Fields().Get(0).Number())
	require.Equal(t, protoreflect.Name("action_event"), eventOneof.Fields().Get(1).Name())
	require.Equal(t, protoreflect.FieldNumber(2), eventOneof.Fields().Get(1).Number())

	assertField(t, file, "InstanceMetricSample", "mspt_p95_millis")
	assertField(t, file, "GetInstanceMetricsResponse", "mspt_p95_millis")
	assertMessageFields(t, file, "GetInstanceResourceSnapshotResponse", []string{
		"root_pid", "process_count", "process_rss_bytes", "cpu_percent", "uptime_seconds",
		"rss_available", "cpu_available", "uptime_available", "unavailable_reason",
	})
	assertMessageFields(t, file, "GetBotCapacityResponse", []string{
		"worker_process_rss_bytes", "worker_process_rss_available", "worker_process_rss_unavailable_reason",
	})
}

func TestWorkerProto_FleetOneofMarshalRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		event *workerpb.BotFleetEvent
		check func(*testing.T, *workerpb.BotFleetEvent)
	}{
		{
			name: "runtime snapshot",
			event: &workerpb.BotFleetEvent{Event: &workerpb.BotFleetEvent_RuntimeSnapshot{
				RuntimeSnapshot: &workerpb.BotRuntimeSnapshot{BotUuid: "bot-1", EventSeq: 7},
			}},
			check: func(t *testing.T, got *workerpb.BotFleetEvent) {
				require.IsType(t, &workerpb.BotFleetEvent_RuntimeSnapshot{}, got.Event)
				require.Equal(t, "bot-1", got.GetRuntimeSnapshot().BotUuid)
				require.EqualValues(t, 7, got.GetRuntimeSnapshot().EventSeq)
			},
		},
		{
			name: "action event",
			event: &workerpb.BotFleetEvent{Event: &workerpb.BotFleetEvent_ActionEvent{
				ActionEvent: &workerpb.BotActionEvent{BotUuid: "bot-2", StepId: "step-1"},
			}},
			check: func(t *testing.T, got *workerpb.BotFleetEvent) {
				require.IsType(t, &workerpb.BotFleetEvent_ActionEvent{}, got.Event)
				require.Equal(t, "bot-2", got.GetActionEvent().BotUuid)
				require.Equal(t, "step-1", got.GetActionEvent().StepId)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := proto.Marshal(tt.event)
			require.NoError(t, err)
			var got workerpb.BotFleetEvent
			require.NoError(t, proto.Unmarshal(payload, &got))
			tt.check(t, &got)
		})
	}
}

func assertMessageFields(t *testing.T, file protoreflect.FileDescriptor, message string, fields []string) {
	t.Helper()
	for _, field := range fields {
		assertField(t, file, message, field)
	}
}

func assertField(t *testing.T, file protoreflect.FileDescriptor, message, field string) {
	t.Helper()
	descriptor := file.Messages().ByName(protoreflect.Name(message))
	require.NotNilf(t, descriptor, "缺少冻结消息 %s", message)
	require.NotNilf(t, descriptor.Fields().ByName(protoreflect.Name(field)), "%s 缺少冻结字段 %s", message, field)
}
