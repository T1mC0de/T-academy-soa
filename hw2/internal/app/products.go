package app

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"

	"marketplace/internal/api"
)

// Колонки товара в том порядке, в котором их читает scanProduct.
const productColumns = `id, name, description, price, stock, category, status, seller_id, created_at, updated_at`

// scanner — то, что умеет Scan: и *sql.Row (одна строка), и *sql.Rows (много строк).
type scanner interface {
	Scan(dest ...any) error
}

func scanProduct(row scanner) (api.ProductResponse, error) {
	var p api.ProductResponse
	err := row.Scan(&p.Id, &p.Name, &p.Description, &p.Price, &p.Stock, &p.Category, &p.Status, &p.SellerId, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// POST /products
func (s *Server) CreateProduct(ctx context.Context, req api.CreateProductRequestObject) (api.CreateProductResponseObject, error) {
	userID := req.Params.XUserId
	role := req.Params.XUserRole
	if role != api.SELLER && role != api.ADMIN {
		return nil, errAccessDenied()
	}

	b := req.Body
	product, err := scanProduct(s.db.QueryRow(
		`INSERT INTO products (name, description, price, stock, category, status, seller_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING `+productColumns,
		b.Name, b.Description, b.Price, b.Stock, b.Category, b.Status, userID,
	))
	if err != nil {
		return nil, err
	}
	return api.CreateProduct201JSONResponse(product), nil
}

// GET /products/{id}
func (s *Server) GetProduct(ctx context.Context, req api.GetProductRequestObject) (api.GetProductResponseObject, error) {
	product, err := scanProduct(s.db.QueryRow(`SELECT `+productColumns+` FROM products WHERE id = $1`, req.Id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errProductNotFound(req.Id)
	}
	if err != nil {
		return nil, err
	}
	return api.GetProduct200JSONResponse(product), nil
}

// GET /products?page=&size=&status=&category=
func (s *Server) ListProducts(ctx context.Context, req api.ListProductsRequestObject) (api.ListProductsResponseObject, error) {
	// Значения по умолчанию — как в спецификации.
	page := int32(0)
	size := int32(20)
	if req.Params.Page != nil {
		page = *req.Params.Page
	}
	if req.Params.Size != nil {
		size = *req.Params.Size
	}

	// Фильтр не передан → параметр равен NULL → условие "$1 IS NULL" истинно,
	// то есть фильтр не применяется.
	status := req.Params.Status
	category := req.Params.Category
	const filter = ` WHERE ($1::product_status IS NULL OR status = $1)
	                   AND ($2::text IS NULL OR category = $2)`

	result := api.ProductPage{Items: []api.ProductResponse{}, Page: page, Size: size}

	// Общее количество товаров под фильтром (totalElements).
	err := s.db.QueryRow(`SELECT count(*) FROM products`+filter, status, category).Scan(&result.TotalElements)
	if err != nil {
		return nil, err
	}

	// Одна страница: пропускаем page*size строк и берём size.
	rows, err := s.db.Query(
		`SELECT `+productColumns+` FROM products`+filter+`
		 ORDER BY created_at DESC
		 LIMIT $3 OFFSET $4`,
		status, category, size, page*size,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		product, err := scanProduct(rows)
		if err != nil {
			return nil, err
		}
		result.Items = append(result.Items, product)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return api.ListProducts200JSONResponse(result), nil
}

// PUT /products/{id} — полная замена полей товара.
func (s *Server) UpdateProduct(ctx context.Context, req api.UpdateProductRequestObject) (api.UpdateProductResponseObject, error) {
	if err := s.checkProductAccess(req.Params.XUserId, req.Params.XUserRole, req.Id); err != nil {
		return nil, err
	}

	b := req.Body
	product, err := scanProduct(s.db.QueryRow(
		`UPDATE products
		 SET name = $2, description = $3, price = $4, stock = $5, category = $6, status = $7
		 WHERE id = $1
		 RETURNING `+productColumns,
		req.Id, b.Name, b.Description, b.Price, b.Stock, b.Category, b.Status,
	))
	if err != nil {
		return nil, err
	}
	return api.UpdateProduct200JSONResponse(product), nil
}

// DELETE /products/{id} — мягкое удаление: запись остаётся, статус становится ARCHIVED.
func (s *Server) DeleteProduct(ctx context.Context, req api.DeleteProductRequestObject) (api.DeleteProductResponseObject, error) {
	if err := s.checkProductAccess(req.Params.XUserId, req.Params.XUserRole, req.Id); err != nil {
		return nil, err
	}

	product, err := scanProduct(s.db.QueryRow(
		`UPDATE products SET status = 'ARCHIVED' WHERE id = $1 RETURNING `+productColumns, req.Id))
	if err != nil {
		return nil, err
	}
	return api.DeleteProduct200JSONResponse(product), nil
}

// checkProductAccess: ADMIN может менять любой товар, SELLER — только свой, USER — никакой.
func (s *Server) checkProductAccess(userID uuid.UUID, role api.Role, productID uuid.UUID) error {
	if role != api.SELLER && role != api.ADMIN {
		return errAccessDenied()
	}

	var sellerID uuid.UUID
	err := s.db.QueryRow(`SELECT seller_id FROM products WHERE id = $1`, productID).Scan(&sellerID)
	if errors.Is(err, sql.ErrNoRows) {
		return errProductNotFound(productID)
	}
	if err != nil {
		return err
	}

	if role == api.SELLER && sellerID != userID {
		return errAccessDenied()
	}
	return nil
}
