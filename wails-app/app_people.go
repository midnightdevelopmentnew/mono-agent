package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"monoagent/internal/storage"
)

// ─────────────────────────────────────────────────────────────────────────────
// People
// ─────────────────────────────────────────────────────────────────────────────

type PersonInfo struct {
	ID             string `json:"id"`
	Username       string `json:"username"`
	Platform       string `json:"platform"`
	FullName       string `json:"full_name"`
	ImageURL       string `json:"image_url"`
	ProfileURL     string `json:"profile_url"`
	FollowerCount  string `json:"follower_count"`
	FollowingCount int    `json:"following_count"`
	IsVerified     bool   `json:"is_verified"`
	JobTitle       string `json:"job_title"`
	Category       string `json:"category"`
	CreatedAt      string `json:"created_at"`
}

type PersonDetailInfo struct {
	ID             string `json:"id"`
	Username       string `json:"username"`
	Platform       string `json:"platform"`
	FullName       string `json:"full_name"`
	ImageURL       string `json:"image_url"`
	ProfileURL     string `json:"profile_url"`
	FollowerCount  string `json:"follower_count"`
	FollowingCount int    `json:"following_count"`
	ContentCount   int    `json:"content_count"`
	IsVerified     bool   `json:"is_verified"`
	JobTitle       string `json:"job_title"`
	Category       string `json:"category"`
	Introduction   string `json:"introduction"`
	Website        string `json:"website"`
	ContactDetails string `json:"contact_details"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

type PersonInteraction struct {
	ActionID         string `json:"action_id"`
	ActionTitle      string `json:"action_title"`
	ActionType       string `json:"action_type"`
	Platform         string `json:"platform"`
	Link             string `json:"link"`
	Status           string `json:"status"`
	CommentText      string `json:"comment_text"`
	SourceType       string `json:"source_type"`
	LastInteractedAt string `json:"last_interacted_at"`
	CreatedAt        string `json:"created_at"`
}

// PostSummary is returned by GetPersonPosts.
type PostSummary struct {
	ID           string `json:"id"`
	Shortcode    string `json:"shortcode"`
	URL          string `json:"url"`
	ThumbnailURL string `json:"thumbnail_url"`
	LikeCount    int    `json:"like_count"`
	CommentCount int    `json:"comment_count"`
	Caption      string `json:"caption"`
	PostedAt     string `json:"posted_at"`
	ScrapedAt    string `json:"scraped_at"`
	WeLiked      bool   `json:"we_liked"`
	WeCommented  bool   `json:"we_commented"`
}

// PostDetail is returned by GetPostDetail.
type PostDetail struct {
	ID           string `json:"id"`
	Shortcode    string `json:"shortcode"`
	URL          string `json:"url"`
	ThumbnailURL string `json:"thumbnail_url"`
	LikeCount    int    `json:"like_count"`
	CommentCount int    `json:"comment_count"`
	Caption      string `json:"caption"`
	PostedAt     string `json:"posted_at"`
	ScrapedAt    string `json:"scraped_at"`
}

// PostComment is returned by GetPostComments.
type PostComment struct {
	ID         string `json:"id"`
	Author     string `json:"author"`
	Text       string `json:"text"`
	Timestamp  string `json:"timestamp"`
	LikesCount int    `json:"likes_count"`
	ReplyCount int    `json:"reply_count"`
}

func (a *App) GetPeople(platform, search string, limit, offset int) []PersonInfo {
	if a.db == nil {
		return nil
	}
	query := `SELECT id, platform_username, platform, COALESCE(full_name,''), COALESCE(image_url,''),
	                 COALESCE(profile_url,''), COALESCE(follower_count,''), COALESCE(following_count,0), COALESCE(is_verified,0),
	                 COALESCE(job_title,''), COALESCE(category,''), COALESCE(created_at,'')
	          FROM people WHERE profile_id = ?`
	var args []interface{}
	args = append(args, a.getActiveProfileID())
	if platform != "" && platform != "ALL" {
		query += " AND UPPER(platform) = ?"
		args = append(args, strings.ToUpper(platform))
	}
	if search != "" {
		query += " AND (platform_username LIKE ? OR full_name LIKE ?)"
		s := "%" + search + "%"
		args = append(args, s, s)
	}
	query += " ORDER BY created_at DESC"
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
	}

	rows, err := a.db.Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var people []PersonInfo
	for rows.Next() {
		var p PersonInfo
		var isVerified int
		if rows.Scan(&p.ID, &p.Username, &p.Platform, &p.FullName, &p.ImageURL,
			&p.ProfileURL, &p.FollowerCount, &p.FollowingCount, &isVerified, &p.JobTitle, &p.Category, &p.CreatedAt) == nil {
			p.IsVerified = isVerified == 1
			people = append(people, p)
		}
	}
	return people
}

func (a *App) GetPeopleCount(platform, search string) int {
	if a.db == nil {
		return 0
	}
	query := "SELECT COUNT(*) FROM people WHERE profile_id = ?"
	var args []interface{}
	args = append(args, a.getActiveProfileID())
	if platform != "" && platform != "ALL" {
		query += " AND UPPER(platform) = ?"
		args = append(args, strings.ToUpper(platform))
	}
	if search != "" {
		query += " AND (platform_username LIKE ? OR full_name LIKE ?)"
		s := "%" + search + "%"
		args = append(args, s, s)
	}
	var count int
	_ = a.db.QueryRow(query, args...).Scan(&count)
	return count
}

func (a *App) GetPersonDetail(id string) *PersonDetailInfo {
	if a.db == nil {
		return nil
	}
	row := a.db.QueryRow(`
		SELECT id, platform_username, platform,
		       COALESCE(full_name,''), COALESCE(image_url,''), COALESCE(profile_url,''),
		       COALESCE(follower_count,''), COALESCE(following_count,0), COALESCE(content_count,0), COALESCE(is_verified,0),
		       COALESCE(job_title,''), COALESCE(category,''),
		       COALESCE(introduction,''), COALESCE(website,''), COALESCE(contact_details,''),
		       COALESCE(created_at,''), COALESCE(updated_at,'')
		FROM people WHERE id = ? AND profile_id = ?`, id, a.getActiveProfileID())
	var p PersonDetailInfo
	var isVerified int
	if err := row.Scan(&p.ID, &p.Username, &p.Platform,
		&p.FullName, &p.ImageURL, &p.ProfileURL,
		&p.FollowerCount, &p.FollowingCount, &p.ContentCount, &isVerified,
		&p.JobTitle, &p.Category,
		&p.Introduction, &p.Website, &p.ContactDetails,
		&p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil
	}
	p.IsVerified = isVerified == 1
	return &p
}

func (a *App) GetPersonInteractions(id string) []PersonInteraction {
	if a.db == nil {
		return nil
	}
	rows, err := a.db.Query(`
		SELECT at.action_id, COALESCE(a.title,''), COALESCE(a.type,''),
		       at.platform, COALESCE(at.link,''), at.status,
		       COALESCE(at.comment_text,''), COALESCE(at.source_type,''),
		       COALESCE(at.last_interacted_at,''), COALESCE(at.created_at,'')
		FROM action_targets at
		LEFT JOIN actions a ON at.action_id = a.id
		JOIN people p ON at.person_id = p.id
		WHERE at.person_id = ? AND p.profile_id = ?
		ORDER BY COALESCE(at.last_interacted_at, at.created_at) DESC
		LIMIT 200`, id, a.getActiveProfileID())
	if err != nil {
		return nil
	}
	defer rows.Close()
	var interactions []PersonInteraction
	for rows.Next() {
		var i PersonInteraction
		if rows.Scan(&i.ActionID, &i.ActionTitle, &i.ActionType,
			&i.Platform, &i.Link, &i.Status,
			&i.CommentText, &i.SourceType,
			&i.LastInteractedAt, &i.CreatedAt) == nil {
			interactions = append(interactions, i)
		}
	}
	return interactions
}

// GetPersonMessages returns a person's message/interaction history (from
// Outlook, social platforms, manual notes, ...), delegating to the same
// storage.PersonMessage repo used by `monoagentcli people messages`.
func (a *App) GetPersonMessages(personID string) []*storage.PersonMessage {
	if a.db == nil {
		return nil
	}
	messages, err := (&storage.Database{DB: a.db}).ListPersonMessages(personID, "", a.getActiveProfileID(), 0, 0)
	if err != nil {
		return nil
	}
	return messages
}

// GetAllPersonMessages returns synced messages across every person in the
// active profile, newest first — a unified communications feed, delegating
// to the same repo used by `monoagentcli people messages all`.
func (a *App) GetAllPersonMessages(limit int) []*storage.PersonMessageWithPerson {
	if a.db == nil {
		return nil
	}
	messages, err := (&storage.Database{DB: a.db}).ListAllPersonMessages(a.getActiveProfileID(), "", limit, 0)
	if err != nil {
		return nil
	}
	return messages
}

// AddPersonMessage records a message/interaction for a person, delegating to
// the same storage.PersonMessage repo used by `monoagentcli people messages add`.
func (a *App) AddPersonMessage(personID, source, externalID, direction, sender, subject, body string) error {
	if a.db == nil {
		return fmt.Errorf("database not initialized")
	}
	msg := &storage.PersonMessage{
		PersonID:   personID,
		Source:     source,
		ExternalID: externalID,
		Direction:  direction,
		Sender:     sender,
		Subject:    subject,
		Body:       body,
	}
	return (&storage.Database{DB: a.db}).UpsertPersonMessage(msg, a.getActiveProfileID())
}

// ComposePersonMessage sends (or drafts, when asDraft is true) an email to a
// person via the given Outlook connection, using service.outlook_mail under
// the hood, and records the result on that person's message history.
func (a *App) ComposePersonMessage(personID, connectionID, subject, body string, asDraft bool) (*storage.PersonMessage, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	var toAddr string
	if err := a.db.QueryRow(
		`SELECT platform_username FROM people WHERE id = ? AND profile_id = ?`,
		personID, a.getActiveProfileID(),
	).Scan(&toAddr); err != nil {
		return nil, fmt.Errorf("person not found: %w", err)
	}

	operation, status := "send_message", "sent"
	if asDraft {
		operation, status = "create_draft", "draft"
	}

	result := a.RunNode(NodeRunRequest{
		NodeType: "service.outlook_mail",
		Config: map[string]interface{}{
			"credential_id": connectionID,
			"operation":     operation,
			"to":            toAddr,
			"subject":       subject,
			"body":          body,
			"body_type":     "html",
		},
	})
	if result.Error != "" {
		return nil, fmt.Errorf("%s", result.Error)
	}

	var externalID string
	if len(result.Outputs) > 0 && len(result.Outputs[0].Items) > 0 {
		if id, ok := result.Outputs[0].Items[0]["id"].(string); ok {
			externalID = id
		}
	}
	// Remember which connection created this draft so a later send/reject
	// doesn't need the caller to resupply it.
	metaBytes, _ := json.Marshal(map[string]string{"connection_id": connectionID})

	msg := &storage.PersonMessage{
		PersonID:   personID,
		Source:     "outlook",
		ExternalID: externalID,
		Direction:  "outbound",
		Sender:     toAddr,
		Subject:    subject,
		Body:       body,
		Metadata:   string(metaBytes),
		Status:     status,
		SentAt:     time.Now().UTC(),
	}
	db := &storage.Database{DB: a.db}
	if err := db.UpsertPersonMessage(msg, a.getActiveProfileID()); err != nil {
		return nil, err
	}
	return msg, nil
}

// GetDraftPersonMessages returns all draft (unsent) outbound messages for the
// active profile, for review in the Human in Loop section.
func (a *App) GetDraftPersonMessages() ([]*storage.PersonMessageWithPerson, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	return (&storage.Database{DB: a.db}).ListPersonMessagesByStatus(a.getActiveProfileID(), "draft")
}

// draftMessageConnectionID extracts the connection_id stashed in a draft
// message's Metadata by ComposePersonMessage.
func draftMessageConnectionID(msg *storage.PersonMessage) (string, error) {
	var meta struct {
		ConnectionID string `json:"connection_id"`
	}
	if msg.Metadata == "" {
		return "", fmt.Errorf("message %s has no associated connection", msg.ID)
	}
	if err := json.Unmarshal([]byte(msg.Metadata), &meta); err != nil || meta.ConnectionID == "" {
		return "", fmt.Errorf("message %s has no associated connection", msg.ID)
	}
	return meta.ConnectionID, nil
}

// SendDraftPersonMessage sends a previously-created draft (via Graph's
// "send an existing draft" endpoint) and marks it as sent.
func (a *App) SendDraftPersonMessage(personMessageID string) (*storage.PersonMessage, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	db := &storage.Database{DB: a.db}
	msg, err := db.GetPersonMessage(personMessageID)
	if err != nil {
		return nil, err
	}
	if msg == nil {
		return nil, fmt.Errorf("message %s not found", personMessageID)
	}
	if msg.Status != "draft" {
		return nil, fmt.Errorf("message %s is not a draft", personMessageID)
	}
	connectionID, err := draftMessageConnectionID(msg)
	if err != nil {
		return nil, err
	}

	result := a.RunNode(NodeRunRequest{
		NodeType: "service.outlook_mail",
		Config: map[string]interface{}{
			"credential_id": connectionID,
			"operation":     "send_draft",
			"message_id":    msg.ExternalID,
		},
	})
	if result.Error != "" {
		return nil, fmt.Errorf("%s", result.Error)
	}

	if err := db.UpdatePersonMessageStatus(personMessageID, "sent"); err != nil {
		return nil, err
	}
	msg.Status = "sent"
	// Graph reassigns a new message id when a draft is sent (moved into Sent
	// Items), so the stored external_id must be updated to stay valid for a
	// later reply/get_message/delete_message.
	if len(result.Outputs) > 0 && len(result.Outputs[0].Items) > 0 {
		if newID, ok := result.Outputs[0].Items[0]["message_id"].(string); ok && newID != "" && newID != msg.ExternalID {
			if err := db.UpdatePersonMessageExternalID(personMessageID, newID); err != nil {
				return nil, err
			}
			msg.ExternalID = newID
		}
	}
	return msg, nil
}

// RejectDraftPersonMessage deletes a draft message: best-effort removes it
// from the Outlook Drafts folder, then deletes the local history row.
func (a *App) RejectDraftPersonMessage(personMessageID string) error {
	if a.db == nil {
		return fmt.Errorf("database not initialized")
	}
	db := &storage.Database{DB: a.db}
	msg, err := db.GetPersonMessage(personMessageID)
	if err != nil {
		return err
	}
	if msg == nil {
		return fmt.Errorf("message %s not found", personMessageID)
	}
	if connectionID, err := draftMessageConnectionID(msg); err == nil && msg.ExternalID != "" {
		a.RunNode(NodeRunRequest{
			NodeType: "service.outlook_mail",
			Config: map[string]interface{}{
				"credential_id": connectionID,
				"operation":     "delete_message",
				"message_id":    msg.ExternalID,
			},
		})
	}
	return db.DeletePersonMessage(personMessageID)
}

// GetLatestPersonStatus returns the most recent status update for a person,
// or nil if none exists yet — the GUI equivalent of `people status get`.
func (a *App) GetLatestPersonStatus(personId string) *storage.PersonStatusUpdate {
	if a.db == nil {
		return nil
	}
	u, err := (&storage.Database{DB: a.db}).GetLatestPersonStatusUpdate(personId, a.getActiveProfileID())
	if err != nil {
		return nil
	}
	return u
}

// AddPersonStatus appends a new status update for a person, delegating to
// the same storage.PersonStatusUpdate repo used by `monoagentcli people status set`.
func (a *App) AddPersonStatus(personId, text string) (*storage.PersonStatusUpdate, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	return (&storage.Database{DB: a.db}).AddPersonStatusUpdate(personId, a.getActiveProfileID(), text)
}

// GetPersonStatusHistory returns every status update for a person, newest
// first — the GUI equivalent of `people status history`. limit <= 0 means
// no cap.
func (a *App) GetPersonStatusHistory(personId string, limit int) []*storage.PersonStatusUpdate {
	if a.db == nil {
		return nil
	}
	updates, err := (&storage.Database{DB: a.db}).ListPersonStatusUpdates(personId, a.getActiveProfileID(), limit)
	if err != nil {
		return nil
	}
	return updates
}

// GetPersonPosts returns all scraped posts for a person, with we_liked/we_commented flags.
func (a *App) GetPersonPosts(personID string) []PostSummary {
	if a.db == nil {
		return []PostSummary{}
	}
	rows, err := a.db.Query(`
		SELECT
			p.id,
			p.shortcode,
			p.url,
			COALESCE(p.thumbnail_url, ''),
			COALESCE(p.like_count, 0),
			COALESCE(p.comment_count, 0),
			COALESCE(p.caption, ''),
			COALESCE(p.posted_at, ''),
			p.scraped_at,
			EXISTS(
				SELECT 1 FROM action_targets at2
				JOIN actions a2 ON at2.action_id = a2.id
				WHERE rtrim(at2.link, '/') = rtrim(p.url, '/')
				  AND a2.type = 'like_posts'
				  AND at2.status = 'COMPLETED'
			) AS we_liked,
			EXISTS(
				SELECT 1 FROM action_targets at3
				JOIN actions a3 ON at3.action_id = a3.id
				WHERE rtrim(at3.link, '/') = rtrim(p.url, '/')
				  AND a3.type = 'comment_on_posts'
				  AND at3.status = 'COMPLETED'
			) AS we_commented
		FROM posts p
		JOIN people pe ON p.person_id = pe.id
		WHERE p.person_id = ? AND pe.profile_id = ?
		ORDER BY p.scraped_at DESC`,
		personID, a.getActiveProfileID(),
	)
	if err != nil {
		return []PostSummary{}
	}
	defer rows.Close()

	var posts []PostSummary
	for rows.Next() {
		var p PostSummary
		var weLiked, weCommented int
		if err := rows.Scan(
			&p.ID, &p.Shortcode, &p.URL, &p.ThumbnailURL,
			&p.LikeCount, &p.CommentCount, &p.Caption,
			&p.PostedAt, &p.ScrapedAt,
			&weLiked, &weCommented,
		); err != nil {
			continue
		}
		p.WeLiked = weLiked != 0
		p.WeCommented = weCommented != 0
		posts = append(posts, p)
	}
	if err := rows.Err(); err != nil {
		return []PostSummary{}
	}
	if posts == nil {
		return []PostSummary{}
	}
	return posts
}

// GetPostDetail returns full metadata for a single post by ID.
func (a *App) GetPostDetail(postID string) *PostDetail {
	if a.db == nil {
		return nil
	}
	var p PostDetail
	err := a.db.QueryRow(`
		SELECT posts.id, shortcode, url,
		       COALESCE(thumbnail_url, ''),
		       COALESCE(like_count, 0),
		       COALESCE(comment_count, 0),
		       COALESCE(caption, ''),
		       COALESCE(posted_at, ''),
		       scraped_at
		FROM posts
		JOIN people ON posts.person_id = people.id
		WHERE posts.id = ? AND people.profile_id = ?`,
		postID, a.getActiveProfileID(),
	).Scan(
		&p.ID, &p.Shortcode, &p.URL, &p.ThumbnailURL,
		&p.LikeCount, &p.CommentCount, &p.Caption,
		&p.PostedAt, &p.ScrapedAt,
	)
	if err != nil {
		return nil
	}
	return &p
}

// GetPostComments returns all scraped comments for a post, ordered by timestamp.
func (a *App) GetPostComments(postID string) []PostComment {
	if a.db == nil {
		return []PostComment{}
	}
	rows, err := a.db.Query(`
		SELECT post_comments.id, COALESCE(author, ''), COALESCE(text, ''),
		       COALESCE(timestamp, ''),
		       COALESCE(likes_count, 0),
		       COALESCE(reply_count, 0)
		FROM post_comments
		JOIN posts ON post_comments.post_id = posts.id
		JOIN people ON posts.person_id = people.id
		WHERE post_id = ? AND people.profile_id = ?
		ORDER BY timestamp ASC`,
		postID, a.getActiveProfileID(),
	)
	if err != nil {
		return []PostComment{}
	}
	defer rows.Close()

	var comments []PostComment
	for rows.Next() {
		var c PostComment
		if err := rows.Scan(
			&c.ID, &c.Author, &c.Text,
			&c.Timestamp, &c.LikesCount, &c.ReplyCount,
		); err != nil {
			continue
		}
		comments = append(comments, c)
	}
	if err := rows.Err(); err != nil {
		return []PostComment{}
	}
	if comments == nil {
		return []PostComment{}
	}
	return comments
}

