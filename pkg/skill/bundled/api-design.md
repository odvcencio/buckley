---
name: api-design
description: REST and GraphQL API design patterns and best practices - create consistent, intuitive, and maintainable APIs. Use when designing new API endpoints or refactoring existing ones.
phase: planning
requires_todo: false
priority: 10
---

# API Design

## Purpose

Well-designed APIs are intuitive, consistent, and future-proof. Good API design reduces friction for developers and makes systems easier to evolve.

## REST API Design

### Resource Modeling

Think in terms of resources (nouns), not actions (verbs):

**Good:**
```
GET    /users          - List users
GET    /users/:id      - Get specific user
POST   /users          - Create user
PUT    /users/:id      - Update user
DELETE /users/:id      - Delete user
```

**Bad:**
```
POST /getUser
POST /createUser
POST /deleteUser
```

### URL Structure

**Guidelines:**
- Use nouns, not verbs
- Use plural for collections (`/users` not `/user`)
- Use hyphens for multi-word resources (`/user-profiles`)
- Keep URLs lowercase
- Nest resources to show relationships (`/users/:id/orders`)
- Limit nesting to 2-3 levels

**Examples:**
```
/products/:id/reviews          - Reviews for a product
/users/:id/orders/:orderId     - Specific order for a user
/organizations/:id/members     - Members of an organization
```

### HTTP Methods

Use standard HTTP methods correctly:

- **GET** - Retrieve resource(s), no side effects, idempotent
- **POST** - Create new resource, not idempotent
- **PUT** - Replace entire resource, idempotent
- **PATCH** - Partially update resource, idempotent
- **DELETE** - Remove resource, idempotent

**Idempotent** means multiple identical requests have the same effect as one request.

### Status Codes

Use appropriate status codes:

**Success:**
- `200 OK` - Request succeeded (GET, PUT, PATCH)
- `201 Created` - Resource created (POST)
- `204 No Content` - Request succeeded, no response body (DELETE)

**Client Errors:**
- `400 Bad Request` - Invalid request data
- `401 Unauthorized` - Authentication required
- `403 Forbidden` - Authenticated but not authorized
- `404 Not Found` - Resource doesn't exist
- `409 Conflict` - Request conflicts with current state
- `422 Unprocessable Entity` - Validation failed
- `429 Too Many Requests` - Rate limit exceeded

**Server Errors:**
- `500 Internal Server Error` - Unexpected server error
- `502 Bad Gateway` - Upstream service failed
- `503 Service Unavailable` - Temporarily unavailable
- `504 Gateway Timeout` - Upstream service timeout

### Request/Response Format

**Consistent JSON structure:**

```json
{
  "data": {
    "id": "123",
    "type": "user",
    "attributes": {
      "name": "Alice",
      "email": "alice@example.com"
    }
  }
}
```

**Error responses:**

```json
{
  "error": {
    "code": "VALIDATION_ERROR",
    "message": "Invalid email format",
    "details": {
      "field": "email",
      "value": "not-an-email"
    }
  }
}
```

### Pagination

For collections, always paginate:

**Cursor-based (recommended for large datasets):**
```
GET /users?cursor=eyJpZCI6MTIzfQ&limit=20

Response:
{
  "data": [...],
  "pagination": {
    "next_cursor": "eyJpZCI6MTQzfQ",
    "has_more": true
  }
}
```

**Offset-based (simpler, but less efficient):**
```
GET /users?page=2&per_page=20

Response:
{
  "data": [...],
  "pagination": {
    "page": 2,
    "per_page": 20,
    "total": 150,
    "total_pages": 8
  }
}
```

### Filtering and Sorting

Support common query needs:

**Filtering:**
```
GET /products?category=electronics&price_max=1000
GET /users?status=active&role=admin
```

**Sorting:**
```
GET /products?sort=price:asc
GET /products?sort=-created_at  (- prefix for descending)
```

**Field selection:**
```
GET /users?fields=id,name,email
```

### Versioning

Version your APIs to enable evolution:

**URL versioning (simple, clear):**
```
/v1/users
/v2/users
```

**Header versioning (cleaner URLs):**
```
Accept: application/vnd.myapi.v1+json
```

**Guidelines:**
- Start with v1, not v0
- Maintain old versions for reasonable period
- Document deprecation timeline
- Major breaking changes → new version
- Minor additions → same version

## GraphQL API Design

### Schema Design

**Type-first thinking:**

```graphql
type User {
  id: ID!
  name: String!
  email: String!
  posts: [Post!]!
  createdAt: DateTime!
}

type Post {
  id: ID!
  title: String!
  content: String!
  author: User!
  comments: [Comment!]!
}

type Query {
  user(id: ID!): User
  users(limit: Int, offset: Int): [User!]!
  post(id: ID!): Post
}

type Mutation {
  createUser(input: CreateUserInput!): User!
  updateUser(id: ID!, input: UpdateUserInput!): User!
  deleteUser(id: ID!): Boolean!
}
```

### Naming Conventions

- **Queries** - Nouns for resources (`user`, `posts`, `organization`)
- **Mutations** - Verbs + nouns (`createUser`, `updatePost`, `deleteComment`)
- **Types** - Singular PascalCase (`User`, `Post`, `Comment`)
- **Fields** - camelCase (`firstName`, `createdAt`)
- **Enums** - SCREAMING_SNAKE_CASE (`USER_ROLE_ADMIN`)

### Input Types

Use input types for complex arguments:

```graphql
input CreateUserInput {
  name: String!
  email: String!
  password: String!
}

input UpdateUserInput {
  name: String
  email: String
}

mutation {
  createUser(input: {
    name: "Alice"
    email: "alice@example.com"
    password: "secret"
  }) {
    id
    name
  }
}
```

### Error Handling

**Field-level errors:**

```graphql
type UserPayload {
  user: User
  errors: [UserError!]
}

type UserError {
  field: String!
  message: String!
}

mutation {
  createUser(input: {...}) {
    user { id name }
    errors { field message }
  }
}
```

## General Principles

### Consistency

- **Naming** - Use consistent terminology across endpoints
- **Structure** - Response format should be predictable
- **Behavior** - Similar operations should work similarly

### Documentation

Document your API:
- Endpoint purpose and behavior
- Request/response examples
- Error scenarios
- Authentication requirements
- Rate limits

Use OpenAPI (Swagger) for REST or GraphQL schema + descriptions.

### Security

- **Authentication** - Require authentication for protected resources
- **Authorization** - Check permissions for each request
- **Input validation** - Validate and sanitize all inputs
- **Rate limiting** - Prevent abuse
- **HTTPS only** - Encrypt all traffic
- **CORS** - Configure properly for browser clients

### Performance

- **Caching** - Use ETags and Cache-Control headers
- **Compression** - gzip/brotli responses
- **Pagination** - Don't return unbounded lists
- **N+1 prevention** - Use dataloaders in GraphQL, eager loading in SQL

## Anti-Patterns

- **RPC-style URLs** - `/getUser`, `/createOrder` (use proper REST)
- **Inconsistent naming** - `user_id` in one place, `userId` in another
- **Ignoring status codes** - Returning 200 for errors
- **No versioning** - Breaking changes break clients
- **Unbounded responses** - Returning 10,000 items without pagination
- **Exposing internal IDs** - Use UUIDs or opaque tokens for external APIs
- **No error details** - Generic "error occurred" messages
- **Overfetching** - Returning more data than clients need (use field selection)
