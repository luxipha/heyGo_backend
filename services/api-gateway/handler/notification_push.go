package handler

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cprakhar/uber-clone/shared/observe/logs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const fcmScope = "https://www.googleapis.com/auth/firebase.messaging"
const fcmTokenGrant = "urn:ietf:params:oauth:grant-type:jwt-bearer"

type firebaseServiceAccount struct {
	ProjectID   string `json:"project_id"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

type fcmSender struct {
	account      firebaseServiceAccount
	projectID    string
	privateKey   *rsa.PrivateKey
	tokenURL     string
	apiBaseURL   string
	client       *http.Client
	mu           sync.Mutex
	token        string
	tokenExpires time.Time
}

type fcmMessage struct {
	Token        string `json:"token"`
	Notification struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	} `json:"notification"`
	Data map[string]string `json:"data"`
}

var errFCMUnregistered = errors.New("fcm token is unregistered")

func newFCMSenderFromEnv() (*fcmSender, error) {
	path := strings.TrimSpace(os.Getenv("FCM_SERVICE_ACCOUNT_FILE"))
	if path == "" {
		return nil, errors.New("FCM_SERVICE_ACCOUNT_FILE is not configured")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read FCM service account file: %w", err)
	}
	var account firebaseServiceAccount
	if err := json.Unmarshal(contents, &account); err != nil {
		return nil, fmt.Errorf("parse FCM service account file: %w", err)
	}
	block, _ := pem.Decode([]byte(account.PrivateKey))
	if block == nil {
		return nil, errors.New("FCM service account private key is invalid")
	}
	var key *rsa.PrivateKey
	if parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		var ok bool
		key, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("FCM service account key is not RSA")
		}
	} else if key, err = x509.ParsePKCS1PrivateKey(block.Bytes); err != nil {
		return nil, errors.New("FCM service account private key is invalid")
	}
	if account.ProjectID == "" || account.ClientEmail == "" || account.TokenURI == "" {
		return nil, errors.New("FCM service account is missing project_id, client_email, or token_uri")
	}
	projectID := strings.TrimSpace(os.Getenv("FCM_PROJECT_ID"))
	if projectID == "" {
		projectID = account.ProjectID
	}
	return &fcmSender{account: account, projectID: projectID, privateKey: key, tokenURL: account.TokenURI, apiBaseURL: "https://fcm.googleapis.com/v1", client: &http.Client{Timeout: 12 * time.Second}}, nil
}

func (s *fcmSender) accessToken(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && time.Until(s.tokenExpires) > time.Minute {
		return s.token, nil
	}
	now := time.Now().UTC()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{"iss": s.account.ClientEmail, "scope": fcmScope, "aud": s.tokenURL, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	hash := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, s.privateKey, crypto.SHA256, hash[:])
	if err != nil {
		return "", fmt.Errorf("sign FCM service assertion: %w", err)
	}
	assertion := unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
	form := url.Values{"grant_type": {fcmTokenGrant}, "assertion": {assertion}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("get FCM access token: %w", err)
	}
	defer response.Body.Close()
	var tokenResponse struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("FCM OAuth token endpoint returned HTTP %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&tokenResponse); err != nil || tokenResponse.AccessToken == "" {
		return "", errors.New("FCM OAuth token response is invalid")
	}
	if tokenResponse.ExpiresIn < 120 {
		tokenResponse.ExpiresIn = 3600
	}
	s.token = tokenResponse.AccessToken
	s.tokenExpires = now.Add(time.Duration(tokenResponse.ExpiresIn) * time.Second)
	return s.token, nil
}

func (s *fcmSender) send(ctx context.Context, token, title, body, notificationID, notificationType string) error {
	accessToken, err := s.accessToken(ctx)
	if err != nil {
		return err
	}
	message := fcmMessage{Token: token, Data: map[string]string{"notification_id": notificationID, "notification_type": notificationType}}
	message.Notification.Title, message.Notification.Body = title, body
	payload, err := json.Marshal(map[string]any{"message": message})
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(s.apiBaseURL, "/") + "/projects/" + url.PathEscape(s.projectID) + "/messages:send"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("send FCM message: %w", err)
	}
	defer response.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	if response.StatusCode == http.StatusNotFound || strings.Contains(string(responseBody), `"UNREGISTERED"`) {
		return errFCMUnregistered
	}
	return fmt.Errorf("FCM send returned HTTP %d", response.StatusCode)
}

type pendingPushDelivery struct {
	ID             int64
	NotificationID string
	DeviceID       string
	Token          string
	Title          string
	Body           string
	Type           string
	Data           json.RawMessage
	Attempts       int
}

func StartNotificationPushJobs(ctx context.Context, pool *pgxpool.Pool) {
	if strings.TrimSpace(os.Getenv("FCM_SERVICE_ACCOUNT_FILE")) == "" {
		logs.L().Infow("FCM push delivery is disabled; inbox and live socket notifications remain active")
		return
	}
	sender, err := newFCMSenderFromEnv()
	if err != nil {
		logs.L().Errorw("FCM push delivery is disabled because its service account configuration is invalid", "error", err)
		return
	}
	go runNotificationPushWorker(ctx, pool, sender)
}

func runNotificationPushWorker(ctx context.Context, pool *pgxpool.Pool, sender *fcmSender) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		for {
			job, err := claimPushDelivery(ctx, pool)
			if err != nil {
				logs.L().Warnw("failed to claim FCM delivery job", "error", err)
				break
			}
			if job == nil {
				break
			}
			err = sender.send(ctx, job.Token, job.Title, job.Body, job.NotificationID, job.Type)
			if err != nil {
				logs.L().Warnw("FCM notification delivery failed", "deliveryID", job.ID, "attempt", job.Attempts, "error", err)
			}
			finishPushDelivery(ctx, pool, *job, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func claimPushDelivery(ctx context.Context, pool *pgxpool.Pool) (*pendingPushDelivery, error) {
	return claimPushDeliveryForDriver(ctx, pool, "")
}

func claimPushDeliveryForDriver(ctx context.Context, pool *pgxpool.Pool, driverID string) (*pendingPushDelivery, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var job pendingPushDelivery
	err = tx.QueryRow(ctx, `WITH candidate AS (
		SELECT p.id FROM driver_notification_push_deliveries p
		WHERE ($1::UUID IS NULL OR EXISTS(SELECT 1 FROM driver_devices owner_device WHERE owner_device.id=p.device_id AND owner_device.driver_id=$1::UUID))
		AND ((p.status='pending' AND p.next_attempt_at<=NOW()) OR (p.status='processing' AND p.lease_until<NOW()))
		ORDER BY p.id LIMIT 1 FOR UPDATE SKIP LOCKED
	), claimed AS (
		UPDATE driver_notification_push_deliveries p SET status='processing',attempts=p.attempts+1,lease_until=NOW()+INTERVAL '1 minute'
		FROM candidate c WHERE p.id=c.id RETURNING p.id,p.notification_id,p.device_id,p.attempts
	)
	SELECT c.id::BIGINT,c.notification_id::TEXT,c.device_id::TEXT,d.push_token,n.title,n.message,n.type,n.data,c.attempts
	FROM claimed c JOIN driver_devices d ON d.id=c.device_id JOIN driver_notifications n ON n.id=c.notification_id`, nullableNotificationDriverID(driverID)).Scan(&job.ID, &job.NotificationID, &job.DeviceID, &job.Token, &job.Title, &job.Body, &job.Type, &job.Data, &job.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Commit(ctx)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &job, nil
}

func nullableNotificationDriverID(driverID string) any {
	if strings.TrimSpace(driverID) == "" {
		return nil
	}
	return driverID
}

func finishPushDelivery(ctx context.Context, pool *pgxpool.Pool, job pendingPushDelivery, sendErr error) {
	if sendErr == nil {
		_, _ = pool.Exec(ctx, `UPDATE driver_notification_push_deliveries SET status='sent',sent_at=NOW(),lease_until=NULL,last_error='' WHERE id=$1`, job.ID)
		return
	}
	if errors.Is(sendErr, errFCMUnregistered) {
		_, _ = pool.Exec(ctx, `DELETE FROM driver_devices WHERE id=$1::UUID`, job.DeviceID)
		_, _ = pool.Exec(ctx, `UPDATE driver_notification_push_deliveries SET status='dead',lease_until=NULL,last_error='unregistered_device' WHERE id=$1`, job.ID)
		return
	}
	if job.Attempts >= 8 {
		_, _ = pool.Exec(ctx, `UPDATE driver_notification_push_deliveries SET status='dead',lease_until=NULL,last_error='provider_delivery_failed' WHERE id=$1`, job.ID)
		return
	}
	delay := time.Second * time.Duration(1<<min(job.Attempts, 10))
	_, _ = pool.Exec(ctx, `UPDATE driver_notification_push_deliveries SET status='pending',lease_until=NULL,next_attempt_at=NOW()+$2::INTERVAL,last_error='provider_delivery_failed' WHERE id=$1`, job.ID, delay.String())
}
