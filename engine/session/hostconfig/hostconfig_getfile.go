package hostconfig

import (
	context "context"
	"os"
	"path/filepath"
)

func (s HostConfigAttachable) GetFile(ctx context.Context, req *HostConfigRequest) (*HostConfigResponse, error) {
	// Look up the well-known config name
	relPath, ok := WellKnownHostConfigs[req.Name]
	if !ok {
		return newErrorResponse(UNKNOWN, "unknown config name: "+req.Name), nil
	}

	// Resolve the user's home directory
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return newErrorResponse(READ_FAILED, "cannot resolve home directory: "+err.Error()), nil
	}

	// Read the file
	fullPath := filepath.Join(homeDir, relPath)
	content, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return newErrorResponse(NOT_FOUND, "file not found: "+fullPath), nil
		}
		return newErrorResponse(READ_FAILED, "cannot read file: "+err.Error()), nil
	}

	return &HostConfigResponse{
		Result: &HostConfigResponse_File{
			File: &FileContent{
				Content: content,
				Path:    fullPath,
			},
		},
	}, nil
}

func newErrorResponse(errorType ErrorInfo_ErrorType, message string) *HostConfigResponse {
	return &HostConfigResponse{
		Result: &HostConfigResponse_Error{
			Error: &ErrorInfo{
				Type:    errorType,
				Message: message,
			},
		},
	}
}
