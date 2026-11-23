-- What a Hero's venue card needs: the area, the owner's number for a WhatsApp
-- invite (entered by the Hero), and which venue an earning came from.
ALTER TABLE hero_venue_submissions ADD COLUMN area TEXT CHECK(area IS NULL OR length(area)<=80);
ALTER TABLE hero_venue_submissions ADD COLUMN owner_phone TEXT CHECK(owner_phone IS NULL OR length(owner_phone)<=20);
ALTER TABLE hero_venue_earnings ADD COLUMN venue_ref TEXT;
CREATE INDEX hero_venue_earnings_venue ON hero_venue_earnings(venue_ref) WHERE venue_ref IS NOT NULL;
