CREATE TABLE support_case_requests (
 owner_id UUID NOT NULL REFERENCES users(id),
 client_request_id UUID NOT NULL,
 case_id UUID NOT NULL REFERENCES support_cases(id) ON DELETE CASCADE,
 payload JSONB NOT NULL,
 PRIMARY KEY(owner_id,client_request_id)
);
