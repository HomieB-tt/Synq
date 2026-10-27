package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

func roomPath(roomID, tail string) string { return "/rooms/" + url.PathEscape(roomID) + tail }

// RoomMember is one person in a room as the server lists them. Status is
// "JOINED" or "INVITED".
type RoomMember struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Status   string `json:"status"`
}

// RoomSummary is one row of ListRooms: a room you are in or invited to.
// Status is yours: "JOINED" or "INVITED".
type RoomSummary struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	OwnerID string `json:"owner_id"`
	Status  string `json:"status"`
}

// Room is the detail view. Members is empty for someone who has only been
// invited.
type Room struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	OwnerID    string       `json:"owner_id"`
	Status     string       `json:"status"`
	Members    []RoomMember `json:"members"`
	MaxMembers int          `json:"max_members"`
	// PendingOwnerID is set while an ownership offer is waiting for an
	// answer.
	PendingOwnerID string `json:"pending_owner_id"`
}

// CreateRoom makes a room with the caller as its owner.
func (c *Client) CreateRoom(ctx context.Context, accessToken, name string) (*Room, error) {
	var resp Room
	err := c.do(ctx, http.MethodPost, "/rooms", accessToken, map[string]string{"name": name}, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListRooms returns the rooms the caller is in or invited to.
func (c *Client) ListRooms(ctx context.Context, accessToken string) ([]RoomSummary, error) {
	var resp []RoomSummary
	err := c.do(ctx, http.MethodGet, "/rooms", accessToken, nil, &resp)
	return resp, err
}

// GetRoom fetches one room's detail.
func (c *Client) GetRoom(ctx context.Context, accessToken, roomID string) (*Room, error) {
	var resp Room
	err := c.do(ctx, http.MethodGet, "/rooms/"+url.PathEscape(roomID), accessToken, nil, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// InviteToRoom invites a user the caller has an active node with. Only the
// owner can.
func (c *Client) InviteToRoom(ctx context.Context, accessToken, roomID, username string) error {
	return c.do(ctx, http.MethodPost, "/rooms/"+url.PathEscape(roomID)+"/invite", accessToken,
		map[string]string{"username": username}, nil)
}

// AcceptRoomInvite joins a room the caller was invited to.
func (c *Client) AcceptRoomInvite(ctx context.Context, accessToken, roomID string) error {
	return c.do(ctx, http.MethodPost, "/rooms/"+url.PathEscape(roomID)+"/accept", accessToken, nil, nil)
}

// LeaveRoom leaves a room, or declines an invitation to one. The owner
// can't; they end the room instead.
func (c *Client) LeaveRoom(ctx context.Context, accessToken, roomID string) error {
	return c.do(ctx, http.MethodPost, "/rooms/"+url.PathEscape(roomID)+"/leave", accessToken, nil, nil)
}

// EndRoom ends a room for everyone. Only the owner can.
func (c *Client) EndRoom(ctx context.Context, accessToken, roomID string) error {
	return c.do(ctx, http.MethodDelete, "/rooms/"+url.PathEscape(roomID), accessToken, nil, nil)
}

// KickFromRoom removes a member, or withdraws an invitation. Only the
// owner can.
func (c *Client) KickFromRoom(ctx context.Context, accessToken, roomID, userID string) error {
	return c.do(ctx, http.MethodPost, roomPath(roomID, "/kick"), accessToken,
		map[string]string{"user_id": userID}, nil)
}

// OfferRoomTransfer offers ownership to a joined member. Only the owner can.
func (c *Client) OfferRoomTransfer(ctx context.Context, accessToken, roomID, userID string) error {
	return c.do(ctx, http.MethodPost, roomPath(roomID, "/transfer"), accessToken,
		map[string]string{"user_id": userID}, nil)
}

// CancelRoomTransfer withdraws an ownership offer (owner) or declines one
// (the member it was offered to).
func (c *Client) CancelRoomTransfer(ctx context.Context, accessToken, roomID string) error {
	return c.do(ctx, http.MethodDelete, roomPath(roomID, "/transfer"), accessToken, nil, nil)
}

// AcceptRoomTransfer takes the room that was offered to the caller.
func (c *Client) AcceptRoomTransfer(ctx context.Context, accessToken, roomID string) error {
	return c.do(ctx, http.MethodPost, roomPath(roomID, "/transfer/accept"), accessToken, nil, nil)
}

// UploadRoomFile puts size bytes of ciphertext from body into a room as a
// room-owned file and returns its id. sha256 is "sha256:<hex>" of the
// ciphertext. The body is streamed, never read into memory. 409 means the
// room is full of files (20, or 300 MB).
func (c *Client) UploadRoomFile(ctx context.Context, accessToken, roomID string, body io.Reader, size int64, sha256 string) (string, error) {
	q := url.Values{"size": {strconv.FormatInt(size, 10)}, "sha256": {sha256}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+roomPath(roomID, "/files")+"?"+q.Encode(), io.NopCloser(body))
	if err != nil {
		return "", fmt.Errorf("api: build upload request: %w", err)
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.stream.Do(req)
	if err != nil {
		return "", fmt.Errorf("api: upload: %w", err)
	}
	defer resp.Body.Close()
	if err := statusError(resp); err != nil {
		return "", err
	}
	var out struct {
		FileID string `json:"file_id"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil || out.FileID == "" {
		return "", fmt.Errorf("api: upload reply had no file id")
	}
	return out.FileID, nil
}

// DownloadRoomFile opens a room file's ciphertext stream. The caller must
// Close it, and check it read exactly the expected size.
func (c *Client) DownloadRoomFile(ctx context.Context, accessToken, roomID, fileID string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.BaseURL+roomPath(roomID, "/files/"+url.PathEscape(fileID)), nil)
	if err != nil {
		return nil, fmt.Errorf("api: build download request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := c.stream.Do(req)
	if err != nil {
		return nil, fmt.Errorf("api: download: %w", err)
	}
	if err := statusError(resp); err != nil {
		resp.Body.Close()
		return nil, err
	}
	return resp.Body, nil
}
