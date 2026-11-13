package handler

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	gatewayauth "github.com/luxipha/heyGo_backend/services/api-gateway/auth"
	shareddb "github.com/luxipha/heyGo_backend/shared/db"
)

type otpBudgetTestSender func(context.Context, string, string) error

func (f otpBudgetTestSender) SendOTP(c context.Context, p, s string) error { return f(c, p, s) }
func otpBudgetTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	p, e := shareddb.NewPostgresPool(context.Background(), shareddb.Config{URL: dsn, MaxConns: 10})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(p.Close)
	if e = shareddb.Migrate(context.Background(), p); e != nil {
		t.Fatal(e)
	}
	return p
}
func TestOTPBudgetsConcurrentDestinationRequests(t *testing.T) {
	p := otpBudgetTestPool(t)
	a := &phoneAuthAPI{pool: p, pepper: []byte(strings.Repeat("p", 32))}
	r := gin.New()
	var sends, accepted, blocked atomic.Int32
	dest := "audit-" + uuid.NewString() + "@example.invalid"
	r.POST("/request", func(c *gin.Context) {
		a.requestPurposeChallenge(c, "email", dest, "login", nil, func(context.Context, string, string) error { sends.Add(1); return nil })
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("POST", "/request", nil))
			if w.Code == 202 {
				accepted.Add(1)
			} else if w.Code == 429 {
				blocked.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 || blocked.Load() != 7 || sends.Load() != 1 {
		t.Fatalf("accepted=%d blocked=%d sends=%d", accepted.Load(), blocked.Load(), sends.Load())
	}
}
func TestOTPBudgetsUncertainDelivery(t *testing.T) {
	p := otpBudgetTestPool(t)
	a := &phoneAuthAPI{pool: p, pepper: []byte(strings.Repeat("p", 32))}
	dest := "audit-" + uuid.NewString() + "@example.invalid"
	sends := 0
	r := gin.New()
	r.POST("/request", func(c *gin.Context) {
		a.requestPurposeChallenge(c, "email", dest, "login", nil, func(context.Context, string, string) error { sends++; return fmt.Errorf("SMS acceptance unresolved") })
	})
	for _, want := range []int{502, 429} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", "/request", nil))
		if w.Code != want {
			t.Fatalf("got %d want %d", w.Code, want)
		}
	}
	var rows int
	if e := p.QueryRow(context.Background(), `SELECT COUNT(*) FROM auth_otp_challenges WHERE destination=$1`, dest).Scan(&rows); e != nil {
		t.Fatal(e)
	}
	if sends != 1 || rows != 1 {
		t.Fatalf("unexpected accounting: sends=%d rows=%d", sends, rows)
	}
}
func TestOTPBudgetsLinkCannotBypassSignIn(t *testing.T) {
	p := otpBudgetTestPool(t)
	ctx := context.Background()
	for idx, role := range []string{"driver", "rider"} {
		t.Run(role, func(t *testing.T) {
			owner := uuid.NewString()
			phone := fmt.Sprintf("+23480%08d", (time.Now().UnixNano()+int64(idx))%100000000)
			if _, e := p.Exec(ctx, `INSERT INTO users(id,roles)VALUES($1::uuid,$2::text[])`, owner, []string{role}); e != nil {
				t.Fatal(e)
			}
			for i := 0; i < 10; i++ {
				if _, e := p.Exec(ctx, `INSERT INTO auth_otp_challenges(id,channel,destination,code_hash,expires_at,created_at)VALUES(gen_random_uuid(),'phone',$1,'synthetic',NOW()+INTERVAL '5 minutes',NOW()-INTERVAL '20 minutes')`, phone); e != nil {
					t.Fatal(e)
				}
			}
			allowed, _, _, e := otpRequestAllowance(ctx, p, "phone", phone)
			if e != nil || allowed {
				t.Fatalf("main limit not reached: %v", e)
			}
			sends := 0
			cfg := PhoneAuthConfig{OTPPepper: []byte(strings.Repeat("p", 32)), Sender: otpBudgetTestSender(func(context.Context, string, string) error { sends++; return nil })}
			r := gin.New()
			r.POST("/link", func(c *gin.Context) {
				c.Set("authenticated-user", gatewayauth.User{ID: owner, Roles: []string{role}})
				if role == "driver" {
					newDriverPhoneAPI(p, cfg).request(c)
				} else {
					newRiderPhoneAPI(p, cfg).request(c)
				}
			})
			w := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/link", strings.NewReader(fmt.Sprintf(`{"phone":%q}`, phone)))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			if w.Code != 429 || sends != 0 {
				t.Fatalf("unexpected limit: status=%d sends=%d", w.Code, sends)
			}
		})
	}
}

func TestOTPBudgetsSharedFlowsAndAccountCosts(t *testing.T) {
	p := otpBudgetTestPool(t)
	ctx := context.Background()
	owner := uuid.NewString()
	if _, e := p.Exec(ctx, `INSERT INTO users(id,roles)VALUES($1::uuid,ARRAY['rider','driver'])`, owner); e != nil {
		t.Fatal(e)
	}
	email := "budget-" + uuid.NewString() + "@example.invalid"
	// A consumed (including failed-delivery) contact challenge still counts.
	if _, e := p.Exec(ctx, `INSERT INTO rider_contact_challenges(id,rider_id,purpose,current_destination,new_destination,new_code_hash,expires_at,consumed_at)VALUES(gen_random_uuid(),$1::uuid,'email_link','',$2,'synthetic',NOW()+INTERVAL '5 minutes',NOW())`, owner, email); e != nil {
		t.Fatal(e)
	}
	if allowed, _, _, e := otpRequestAllowance(ctx, p, "email", email); e != nil || allowed {
		t.Fatalf("email-link bypass: allowed=%v err=%v", allowed, e)
	}
	// Rotating destinations does not reset the account limit, and phone changes
	// reserve both messages rather than treating a two-SMS request as one send.
	for i := 0; i < 4; i++ {
		if _, e := p.Exec(ctx, `INSERT INTO rider_phone_challenges(id,rider_id,destination,code_hash,expires_at,created_at)VALUES(gen_random_uuid(),$1::uuid,$2,'synthetic',NOW()+INTERVAL '5 minutes',NOW()-INTERVAL '20 minutes')`, owner, fmt.Sprintf("+234810000%04d", i)); e != nil {
			t.Fatal(e)
		}
	}
	if allowed, _, _, e := otpBudgetAllowance(ctx, p, "phone", "", owner, 1); e != nil || !allowed {
		t.Fatalf("one send should fit: %v %v", allowed, e)
	}
	if allowed, retry, _, e := otpBudgetAllowance(ctx, p, "phone", "", owner, 2); e != nil || allowed || retry != otpRetryAfterHour {
		t.Fatalf("two sends must not fit: %v %d %v", allowed, retry, e)
	}
	current, next := "+2348190010001", "+2348190010002"
	if _, e := p.Exec(ctx, `INSERT INTO rider_contact_challenges(id,rider_id,purpose,current_destination,new_destination,current_code_hash,new_code_hash,expires_at)VALUES(gen_random_uuid(),$1::uuid,'phone_change',$2,$3,'synthetic','synthetic',NOW()+INTERVAL '5 minutes')`, owner, current, next); e != nil {
		t.Fatal(e)
	}
	for _, dest := range []string{current, next} {
		if allowed, _, _, e := otpRequestAllowance(ctx, p, "phone", dest); e != nil || allowed {
			t.Fatalf("phone-change destination not counted: %v %v", allowed, e)
		}
	}
}

func TestOTPBudgetsConcurrentCrossFlowRequests(t *testing.T) {
	p := otpBudgetTestPool(t)
	ctx := context.Background()
	owner := uuid.NewString()
	phone := fmt.Sprintf("+23481%08d", time.Now().UnixNano()%100000000)
	if _, e := p.Exec(ctx, `INSERT INTO users(id,roles)VALUES($1::uuid,ARRAY['rider'])`, owner); e != nil {
		t.Fatal(e)
	}
	var sends, accepted, blocked atomic.Int32
	sender := otpBudgetTestSender(func(context.Context, string, string) error { sends.Add(1); return nil })
	a := &phoneAuthAPI{pool: p, pepper: []byte(strings.Repeat("p", 32))}
	r := gin.New()
	r.POST("/login", func(c *gin.Context) { a.requestPurposeChallenge(c, "phone", phone, "login", nil, sender.SendOTP) })
	r.POST("/link", func(c *gin.Context) {
		c.Set("authenticated-user", gatewayauth.User{ID: owner, Roles: []string{"rider"}})
		newRiderPhoneAPI(p, PhoneAuthConfig{OTPPepper: a.pepper, Sender: sender}).request(c)
	})
	var wg sync.WaitGroup
	for _, path := range []string{"/login", "/link"} {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			req := httptest.NewRequest("POST", path, strings.NewReader(fmt.Sprintf(`{"phone":%q}`, phone)))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			switch w.Code {
			case 202:
				accepted.Add(1)
			case 429:
				blocked.Add(1)
			default:
				t.Errorf("unexpected status %d", w.Code)
			}
		}(path)
	}
	wg.Wait()
	if sends.Load() != 1 || accepted.Load() != 1 || blocked.Load() != 1 {
		t.Fatalf("sends=%d accepted=%d blocked=%d", sends.Load(), accepted.Load(), blocked.Load())
	}
}

// A recovery request must wait for the owner row before acquiring budget locks.
// Linking already holds that row, then takes the same advisory locks. Reversing
// the order deadlocks when recovery inserts its owner foreign key.
func TestOTPBudgetsRecoveryAndLinkLockOrder(t *testing.T) {
	p := otpBudgetTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	owner, destination := uuid.NewString(), "recovery-"+uuid.NewString()+"@example.invalid"
	if _, err := p.Exec(ctx, `INSERT INTO users(id,roles) VALUES($1::uuid,ARRAY['driver'])`, owner); err != nil {
		t.Fatal(err)
	}
	linking, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer linking.Rollback(context.Background())
	if _, err = linking.Exec(ctx, `SELECT id FROM users WHERE id=$1::uuid FOR UPDATE`, owner); err != nil {
		t.Fatal(err)
	}
	// Bound the test even if a regression lets recovery hold an advisory lock.
	if _, err = linking.Exec(ctx, `SET LOCAL lock_timeout='500ms'`); err != nil {
		t.Fatal(err)
	}
	auth := &phoneAuthAPI{pool: p, pepper: []byte(strings.Repeat("p", 32))}
	router := gin.New()
	var sends atomic.Int32
	router.POST("/recover", func(c *gin.Context) {
		auth.requestPurposeChallenge(c, "email", destination, "pin_reset", &owner, func(context.Context, string, string) error { sends.Add(1); return nil })
	})
	done := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/recover", nil).WithContext(ctx)
		router.ServeHTTP(rec, req)
		done <- rec.Code
	}()
	// Wait until recovery is blocked on either the owner row (correct) or the FK
	// insert (old ordering). Then linking must still be able to reserve its budget.
	for {
		var blocked bool
		if err = p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND (query LIKE 'SELECT id::text FROM users WHERE id=$1::uuid AND active FOR UPDATE%' OR query LIKE 'INSERT INTO auth_otp_challenges%'))`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case code := <-done:
			t.Fatalf("recovery did not wait for owner: %d", code)
		case <-ctx.Done():
			t.Fatal("recovery did not block")
		case <-time.After(10 * time.Millisecond):
		}
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/link", nil).WithContext(ctx)
	if !reserveOTPBudget(c, linking, "email", owner, destination) {
		t.Fatalf("linking could not acquire budget locks while holding owner row: %s", rec.Body.String())
	}
	if err = linking.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 202 || sends.Load() != 1 {
			t.Fatalf("recovery status=%d sends=%d", code, sends.Load())
		}
	case <-ctx.Done():
		t.Fatal("recovery did not complete")
	}
}
