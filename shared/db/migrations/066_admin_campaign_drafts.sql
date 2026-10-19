-- Campaign authoring only. Activation, deliveries and redemptions require a
-- separately approved execution contract; draft records cannot execute.
CREATE TABLE IF NOT EXISTS admin_campaign_drafts (
 id UUID PRIMARY KEY,
 kind TEXT NOT NULL CHECK(kind IN ('promotion','communication')),
 name TEXT NOT NULL CHECK(char_length(trim(name)) BETWEEN 1 AND 160),
 category TEXT NOT NULL,
 audience TEXT NOT NULL CHECK(char_length(trim(audience)) BETWEEN 1 AND 240),
 body TEXT NOT NULL CHECK(char_length(trim(body)) BETWEEN 1 AND 4000),
 budget_kobo BIGINT NOT NULL DEFAULT 0 CHECK(budget_kobo>=0 AND budget_kobo<=1000000000000),
 status TEXT NOT NULL DEFAULT 'draft' CHECK(status='draft'),
 version INTEGER NOT NULL DEFAULT 1 CHECK(version>0),
 created_by UUID NOT NULL REFERENCES admin_users(id),
 updated_by UUID NOT NULL REFERENCES admin_users(id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 CHECK((kind='promotion' AND category IN ('Discount','Referral','Incentive','Credit')) OR
       (kind='communication' AND category IN ('Push','SMS','WhatsApp','Email') AND budget_kobo=0))
);
CREATE INDEX IF NOT EXISTS admin_campaign_drafts_kind_id_idx ON admin_campaign_drafts(kind,id);
