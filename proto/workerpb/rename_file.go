package workerpb

// 手写的 RenameFile 消息类型。
// 正式 protoc 不可用时以此文件补充，待重新生成后可删除。

// RenameFileRequest 重命名文件请求。
type RenameFileRequest struct {
	InstanceUuid string `protobuf:"bytes,1,opt,name=instance_uuid,json=instanceUuid,proto3" json:"instance_uuid,omitempty"`
	OldPath      string `protobuf:"bytes,2,opt,name=old_path,json=oldPath,proto3" json:"old_path,omitempty"`
	NewPath      string `protobuf:"bytes,3,opt,name=new_path,json=newPath,proto3" json:"new_path,omitempty"`
}

func (x *RenameFileRequest) GetInstanceUuid() string {
	if x != nil {
		return x.InstanceUuid
	}
	return ""
}

func (x *RenameFileRequest) GetOldPath() string {
	if x != nil {
		return x.OldPath
	}
	return ""
}

func (x *RenameFileRequest) GetNewPath() string {
	if x != nil {
		return x.NewPath
	}
	return ""
}

// RenameFileResponse 重命名文件响应。
type RenameFileResponse struct {
	Success bool   `protobuf:"varint,1,opt,name=success,proto3" json:"success,omitempty"`
	Error   string `protobuf:"bytes,2,opt,name=error,proto3" json:"error,omitempty"`
}

func (x *RenameFileResponse) GetSuccess() bool {
	if x != nil {
		return x.Success
	}
	return false
}

func (x *RenameFileResponse) GetError() string {
	if x != nil {
		return x.Error
	}
	return ""
}
