package contracts

type APIResponse struct {
	Data any     `json:"data"`
	Meta APIMeta `json:"meta"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type APIMeta struct {
	NextCursor *string `json:"nextCursor"`
}

type APIErrorResponse struct {
	Error APIError `json:"error"`
}
