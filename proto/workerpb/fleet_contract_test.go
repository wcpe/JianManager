package workerpb_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/proto/workerpb"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestWorkerProto_FleetContractFrozen(t *testing.T) {
	file := workerpb.File_proto_worker_proto
	service := file.Services().ByName("WorkerService")
	require.NotNil(t, service)

	for _, name := range []protoreflect.Name{
		"GetBotCapacity",
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

	assertField(t, file, "InstanceMetricSample", "mspt_p95_millis")
	assertField(t, file, "GetInstanceMetricsResponse", "mspt_p95_millis")
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
