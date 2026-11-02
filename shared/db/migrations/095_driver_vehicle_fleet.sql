ALTER TABLE driver_vehicles RENAME TO driver_vehicle_records;
ALTER TABLE driver_vehicle_records ADD COLUMN id UUID NOT NULL DEFAULT gen_random_uuid(), ADD COLUMN is_active BOOLEAN NOT NULL DEFAULT TRUE, ADD COLUMN approval_status TEXT NOT NULL DEFAULT 'pending' CHECK(approval_status IN ('pending','approved','rejected')), ADD COLUMN approved_at TIMESTAMPTZ;
UPDATE driver_vehicle_records v SET approval_status=p.admin_status,approved_at=p.admin_reviewed_at FROM driver_profiles p WHERE p.driver_id=v.driver_id;
ALTER TABLE driver_vehicle_categories DROP CONSTRAINT driver_vehicle_categories_driver_id_fkey;
ALTER TABLE driver_vehicle_records DROP CONSTRAINT driver_vehicles_pkey;
ALTER TABLE driver_vehicle_records ADD PRIMARY KEY(id), ADD UNIQUE(id,driver_id);
CREATE UNIQUE INDEX driver_vehicle_active ON driver_vehicle_records(driver_id) WHERE is_active;
CREATE FUNCTION selected_driver_vehicle(owner_id UUID) RETURNS UUID LANGUAGE SQL STABLE AS $$ SELECT id FROM driver_vehicle_records WHERE driver_id=owner_id AND CASE WHEN NULLIF(current_setting('heygo.vehicle_id',TRUE),'') IS NOT NULL THEN id=NULLIF(current_setting('heygo.vehicle_id',TRUE),'')::uuid ELSE is_active END $$;
CREATE VIEW driver_vehicles AS SELECT * FROM driver_vehicle_records WHERE id=selected_driver_vehicle(driver_id);
ALTER TABLE driver_vehicle_categories ADD COLUMN vehicle_id UUID;
UPDATE driver_vehicle_categories x SET vehicle_id=v.id FROM driver_vehicle_records v WHERE x.driver_id=v.driver_id;
ALTER TABLE driver_vehicle_categories ADD FOREIGN KEY(vehicle_id,driver_id) REFERENCES driver_vehicle_records(id,driver_id);
ALTER TABLE driver_vehicle_categories RENAME TO driver_vehicle_categories_records;
ALTER TABLE driver_vehicle_categories_records DROP CONSTRAINT driver_vehicle_categories_pkey;
ALTER TABLE driver_vehicle_categories_records ADD PRIMARY KEY(vehicle_id,category_id);
CREATE VIEW driver_vehicle_categories AS SELECT * FROM driver_vehicle_categories_records WHERE vehicle_id=selected_driver_vehicle(driver_id);
CREATE FUNCTION bind_driver_vehicle_categories_vehicle() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.vehicle_id IS NULL THEN NEW.vehicle_id:=selected_driver_vehicle(NEW.driver_id); END IF;
 IF NEW.vehicle_id IS NULL THEN RAISE EXCEPTION 'vehicle required'; END IF; RETURN NEW; END $$;
CREATE TRIGGER bind_vehicle BEFORE INSERT ON driver_vehicle_categories_records FOR EACH ROW EXECUTE FUNCTION bind_driver_vehicle_categories_vehicle();
ALTER TABLE driver_documents ADD COLUMN vehicle_id UUID;
UPDATE driver_documents x SET vehicle_id=v.id FROM driver_vehicle_records v WHERE x.driver_id=v.driver_id AND x.type IN ('vehicle_license','roadworthiness','auto_insurance','hackney_permit');
ALTER TABLE driver_documents ADD FOREIGN KEY(vehicle_id,driver_id) REFERENCES driver_vehicle_records(id,driver_id);
ALTER TABLE driver_documents RENAME TO driver_documents_records;
CREATE VIEW driver_documents AS SELECT * FROM driver_documents_records WHERE vehicle_id=selected_driver_vehicle(driver_id) OR (vehicle_id IS NULL AND type NOT IN ('vehicle_license','roadworthiness','auto_insurance','hackney_permit'));
CREATE FUNCTION bind_driver_documents_vehicle() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.type NOT IN ('vehicle_license','roadworthiness','auto_insurance','hackney_permit') THEN NEW.vehicle_id:=NULL; RETURN NEW; END IF;
 IF NEW.vehicle_id IS NULL THEN NEW.vehicle_id:=selected_driver_vehicle(NEW.driver_id); END IF;
 IF NEW.vehicle_id IS NULL THEN RAISE EXCEPTION 'vehicle required'; END IF; RETURN NEW; END $$;
CREATE TRIGGER bind_vehicle BEFORE INSERT ON driver_documents_records FOR EACH ROW EXECUTE FUNCTION bind_driver_documents_vehicle();
ALTER TABLE driver_document_uploads ADD COLUMN vehicle_id UUID;
UPDATE driver_document_uploads x SET vehicle_id=v.id FROM driver_vehicle_records v WHERE x.driver_id=v.driver_id AND x.type IN ('vehicle_license','roadworthiness','auto_insurance','hackney_permit');
ALTER TABLE driver_document_uploads ADD FOREIGN KEY(vehicle_id,driver_id) REFERENCES driver_vehicle_records(id,driver_id);
ALTER TABLE driver_document_uploads RENAME TO driver_document_uploads_records;
CREATE VIEW driver_document_uploads AS SELECT * FROM driver_document_uploads_records WHERE vehicle_id=selected_driver_vehicle(driver_id) OR (vehicle_id IS NULL AND type NOT IN ('vehicle_license','roadworthiness','auto_insurance','hackney_permit'));
CREATE FUNCTION bind_driver_document_uploads_vehicle() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.type NOT IN ('vehicle_license','roadworthiness','auto_insurance','hackney_permit') THEN NEW.vehicle_id:=NULL; RETURN NEW; END IF;
 IF NEW.vehicle_id IS NULL THEN NEW.vehicle_id:=selected_driver_vehicle(NEW.driver_id); END IF;
 IF NEW.vehicle_id IS NULL THEN RAISE EXCEPTION 'vehicle required'; END IF; RETURN NEW; END $$;
CREATE TRIGGER bind_vehicle BEFORE INSERT ON driver_document_uploads_records FOR EACH ROW EXECUTE FUNCTION bind_driver_document_uploads_vehicle();
ALTER TABLE admin_document_uploads ADD COLUMN vehicle_id UUID;
UPDATE admin_document_uploads x SET vehicle_id=v.id FROM driver_vehicle_records v WHERE x.driver_id=v.driver_id AND x.type IN ('vehicle_license','roadworthiness','auto_insurance','hackney_permit');
ALTER TABLE admin_document_uploads ADD FOREIGN KEY(vehicle_id,driver_id) REFERENCES driver_vehicle_records(id,driver_id);
ALTER TABLE admin_document_uploads RENAME TO admin_document_uploads_records;
CREATE VIEW admin_document_uploads AS SELECT * FROM admin_document_uploads_records WHERE vehicle_id=selected_driver_vehicle(driver_id) OR (vehicle_id IS NULL AND type NOT IN ('vehicle_license','roadworthiness','auto_insurance','hackney_permit'));
CREATE FUNCTION bind_admin_document_uploads_vehicle() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.type NOT IN ('vehicle_license','roadworthiness','auto_insurance','hackney_permit') THEN NEW.vehicle_id:=NULL; RETURN NEW; END IF;
 IF NEW.vehicle_id IS NULL THEN NEW.vehicle_id:=selected_driver_vehicle(NEW.driver_id); END IF;
 IF NEW.vehicle_id IS NULL THEN RAISE EXCEPTION 'vehicle required'; END IF; RETURN NEW; END $$;
CREATE TRIGGER bind_vehicle BEFORE INSERT ON admin_document_uploads_records FOR EACH ROW EXECUTE FUNCTION bind_admin_document_uploads_vehicle();
ALTER TABLE driver_inspections ADD COLUMN vehicle_id UUID;
UPDATE driver_inspections x SET vehicle_id=v.id FROM driver_vehicle_records v WHERE x.driver_id=v.driver_id AND ((x.vehicle_revision=v.inspection_revision AND (x.vehicle_snapshot='{}'::jsonb OR x.vehicle_snapshot->>'synthetic'='true')) OR (COALESCE(x.vehicle_snapshot->>'plate','')=v.plate AND COALESCE(x.vehicle_snapshot->>'make','')=v.make AND COALESCE(x.vehicle_snapshot->>'model','')=v.model));
ALTER TABLE driver_inspections ADD FOREIGN KEY(vehicle_id,driver_id) REFERENCES driver_vehicle_records(id,driver_id);
ALTER TABLE driver_inspections RENAME TO driver_inspections_records;
CREATE VIEW driver_inspections AS SELECT * FROM driver_inspections_records WHERE vehicle_id=selected_driver_vehicle(driver_id);
CREATE FUNCTION bind_driver_inspections_vehicle() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.vehicle_id IS NULL THEN NEW.vehicle_id:=selected_driver_vehicle(NEW.driver_id); END IF;
 IF NEW.vehicle_id IS NULL THEN RAISE EXCEPTION 'vehicle required'; END IF; RETURN NEW; END $$;
CREATE TRIGGER bind_vehicle BEFORE INSERT ON driver_inspections_records FOR EACH ROW EXECUTE FUNCTION bind_driver_inspections_vehicle();
ALTER TABLE vehicle_photo_uploads ADD COLUMN vehicle_id UUID;
UPDATE vehicle_photo_uploads x SET vehicle_id=v.id FROM driver_vehicle_records v WHERE x.driver_id=v.driver_id AND x.vehicle_revision=v.inspection_revision;
ALTER TABLE vehicle_photo_uploads ADD FOREIGN KEY(vehicle_id,driver_id) REFERENCES driver_vehicle_records(id,driver_id);
ALTER TABLE vehicle_photo_uploads RENAME TO vehicle_photo_uploads_records;
CREATE VIEW vehicle_photo_uploads AS SELECT * FROM vehicle_photo_uploads_records WHERE vehicle_id=selected_driver_vehicle(driver_id);
CREATE FUNCTION bind_vehicle_photo_uploads_vehicle() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.vehicle_id IS NULL THEN NEW.vehicle_id:=selected_driver_vehicle(NEW.driver_id); END IF;
 IF NEW.vehicle_id IS NULL THEN RAISE EXCEPTION 'vehicle required'; END IF; RETURN NEW; END $$;
CREATE TRIGGER bind_vehicle BEFORE INSERT ON vehicle_photo_uploads_records FOR EACH ROW EXECUTE FUNCTION bind_vehicle_photo_uploads_vehicle();
ALTER TABLE driver_vehicle_photos ADD COLUMN vehicle_id UUID;
UPDATE driver_vehicle_photos x SET vehicle_id=v.id FROM driver_vehicle_records v WHERE x.driver_id=v.driver_id AND x.vehicle_revision=v.inspection_revision;
ALTER TABLE driver_vehicle_photos ADD FOREIGN KEY(vehicle_id,driver_id) REFERENCES driver_vehicle_records(id,driver_id);
ALTER TABLE driver_vehicle_photos RENAME TO driver_vehicle_photos_records;
DROP INDEX driver_vehicle_photos_current;
CREATE UNIQUE INDEX driver_vehicle_photos_current ON driver_vehicle_photos_records(vehicle_id,vehicle_revision,angle) WHERE superseded_at IS NULL AND vehicle_id IS NOT NULL;
CREATE VIEW driver_vehicle_photos AS SELECT * FROM driver_vehicle_photos_records WHERE vehicle_id=selected_driver_vehicle(driver_id);
CREATE FUNCTION bind_driver_vehicle_photos_vehicle() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.vehicle_id IS NULL THEN NEW.vehicle_id:=selected_driver_vehicle(NEW.driver_id); END IF;
 IF NEW.vehicle_id IS NULL THEN RAISE EXCEPTION 'vehicle required'; END IF; RETURN NEW; END $$;
CREATE TRIGGER bind_vehicle BEFORE INSERT ON driver_vehicle_photos_records FOR EACH ROW EXECUTE FUNCTION bind_driver_vehicle_photos_vehicle();
CREATE OR REPLACE FUNCTION driver_category_eligible(owner_id UUID, requested_slug TEXT) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM ride_categories c JOIN driver_vehicles v ON v.driver_id=owner_id
 WHERE c.slug=requested_slug AND c.status='active' AND v.review_status='approved' AND (
 (v.catalogue_model_id IS NULL AND c.legacy AND v.package_slug=c.slug) OR
 (v.catalogue_model_id IS NOT NULL AND EXISTS(SELECT 1 FROM driver_vehicle_categories a
 JOIN vehicle_catalogue m ON m.id=v.catalogue_model_id JOIN vehicle_category_models cm ON cm.model_id=m.id AND cm.category_id=c.id
 WHERE a.driver_id=v.driver_id AND a.category_id=c.id AND m.status='active'
 AND v.model_year BETWEEN m.year_from AND m.year_to AND v.model_year>=c.minimum_year
 AND v.passenger_seats>=c.passengers AND (NOT c.requires_ac OR v.working_ac)))))
$$;

CREATE FUNCTION reviewed_vehicle_ready(owner_id UUID, car_id UUID) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM driver_vehicle_records v WHERE v.id=car_id AND v.driver_id=owner_id AND v.review_status='approved' AND v.approval_status='approved' AND v.approved_at>=v.updated_at
 AND COALESCE((SELECT i.status='completed' AND i.vehicle_revision=v.inspection_revision AND v.approved_at>=i.updated_at FROM driver_inspections_records i WHERE i.vehicle_id=v.id ORDER BY i.created_at DESC,i.id DESC LIMIT 1),FALSE)
 AND NOT EXISTS(SELECT 1 FROM (VALUES('vehicle_license'),('roadworthiness'),('auto_insurance'),('hackney_permit')) required(type) WHERE NOT COALESCE((SELECT d.status='approved' AND (d.expires_at IS NULL OR d.expires_at>NOW()) AND v.approved_at>=d.updated_at FROM driver_documents_records d WHERE d.vehicle_id=v.id AND d.type=required.type ORDER BY d.submitted_at DESC,d.id DESC LIMIT 1),FALSE))) $$;
CREATE OR REPLACE VIEW driver_eligibility AS SELECT u.id AS driver_id,u.active AND (u.phone_verified_at IS NOT NULL OR u.email_verified) AND p.manual_verification_status='approved' AND p.admin_status='approved' AND p.admin_reviewed_at>=p.updated_at AND reviewed_vehicle_ready(u.id,selected_driver_vehicle(u.id)) AND COALESCE((SELECT d.status='approved' AND (d.expires_at IS NULL OR d.expires_at>NOW()) AND p.admin_reviewed_at>=d.updated_at FROM driver_documents_records d WHERE d.driver_id=u.id AND d.type='driver_license' AND d.vehicle_id IS NULL ORDER BY d.submitted_at DESC,d.id DESC LIMIT 1),FALSE) AS eligible_to_drive FROM users u JOIN driver_profiles p ON p.driver_id=u.id;
ALTER TABLE driver_assignments ADD COLUMN vehicle_id UUID REFERENCES driver_vehicle_records(id), ADD COLUMN vehicle_snapshot JSONB;
CREATE FUNCTION bind_offer_vehicle() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN SELECT id,jsonb_build_object('vehicleId',id,'driverId',driver_id,'packageSlug',package_slug,'make',make,'model',model,'modelYear',model_year,'color',color,'plate',plate) INTO NEW.vehicle_id,NEW.vehicle_snapshot FROM driver_vehicle_records WHERE driver_id=NEW.driver_id AND is_active; RETURN NEW; END $$;
CREATE TRIGGER bind_offer_vehicle BEFORE INSERT ON driver_assignments FOR EACH ROW EXECUTE FUNCTION bind_offer_vehicle();
ALTER TABLE trips ADD COLUMN accepted_vehicle_id UUID REFERENCES driver_vehicle_records(id);
CREATE OR REPLACE FUNCTION capture_trip_vehicle_snapshot() RETURNS TRIGGER AS $$
BEGIN
 IF NEW.accepted_vehicle_id IS DISTINCT FROM OLD.accepted_vehicle_id THEN RAISE EXCEPTION 'accepted vehicle identity is immutable'; END IF;
    IF NEW.accepted_vehicle_snapshot IS DISTINCT FROM OLD.accepted_vehicle_snapshot THEN
        RAISE EXCEPTION 'accepted vehicle snapshot is immutable';
    END IF;
    IF NEW.accepted_vehicle_snapshot_at IS DISTINCT FROM OLD.accepted_vehicle_snapshot_at THEN
        RAISE EXCEPTION 'accepted vehicle snapshot time is immutable';
    END IF;
    IF NEW.status='accepted' AND OLD.status IS DISTINCT FROM NEW.status
       AND OLD.accepted_vehicle_snapshot IS NULL AND NEW.assigned_driver_id IS NOT NULL THEN
        SELECT COALESCE((SELECT a.vehicle_snapshot FROM driver_assignments a WHERE a.trip_id=NEW.id AND a.driver_id=NEW.assigned_driver_id ORDER BY a.attempt DESC LIMIT 1),jsonb_build_object(
            'vehicleId',v.id,'driverId',d.id,'packageSlug',COALESCE(NULLIF(v.package_slug,''),d.package_slug),
            'make',v.make,'model',v.model,'modelYear',v.model_year,'color',v.color,
            'plate',COALESCE(NULLIF(v.plate,''),d.car_plate)))
        INTO NEW.accepted_vehicle_snapshot
        FROM drivers d LEFT JOIN driver_vehicle_records v ON v.id=COALESCE((SELECT a.vehicle_id FROM driver_assignments a WHERE a.trip_id=NEW.id AND a.driver_id=d.id ORDER BY a.attempt DESC LIMIT 1),(SELECT av.id FROM driver_vehicle_records av WHERE av.driver_id=d.id AND av.is_active))
        WHERE d.id=NEW.assigned_driver_id;
        IF NEW.accepted_vehicle_snapshot IS NOT NULL THEN
            NEW.accepted_vehicle_snapshot_at := NOW(); NEW.accepted_vehicle_id := (NEW.accepted_vehicle_snapshot->>'vehicleId')::uuid;
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

ALTER TABLE admin_report_uploads ADD COLUMN vehicle_id UUID;
UPDATE admin_report_uploads u SET vehicle_id=v.id FROM driver_vehicle_records v WHERE u.driver_id=v.driver_id;
ALTER TABLE admin_report_uploads ADD FOREIGN KEY(vehicle_id,driver_id) REFERENCES driver_vehicle_records(id,driver_id);
ALTER TABLE admin_report_uploads RENAME TO admin_report_uploads_records;
CREATE VIEW admin_report_uploads AS SELECT * FROM admin_report_uploads_records WHERE vehicle_id=selected_driver_vehicle(driver_id);
CREATE TRIGGER bind_vehicle BEFORE INSERT ON admin_report_uploads_records FOR EACH ROW EXECUTE FUNCTION bind_vehicle_photo_uploads_vehicle();

CREATE OR REPLACE FUNCTION driver_document_reminder_current(payload JSONB) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM driver_documents_records d WHERE d.id=(payload->>'documentId')::uuid AND d.expires_at=(payload->>'expiresAt')::timestamptz AND d.status='approved'
 AND NOT EXISTS(SELECT 1 FROM driver_documents_records newer WHERE newer.driver_id=d.driver_id AND newer.vehicle_id IS NOT DISTINCT FROM d.vehicle_id AND newer.type=d.type AND (newer.submitted_at,newer.id)>(d.submitted_at,d.id))) $$;

CREATE UNIQUE INDEX driver_vehicle_unique_plate ON driver_vehicle_records(driver_id,upper(regexp_replace(plate,'[^a-zA-Z0-9]','','g'))) WHERE plate<>'';
