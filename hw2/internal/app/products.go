package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"marketplace/internal/api"
)

const productColumns = `id, name, description, price, stock, category, status, seller_id, created_at, updated_at`

func scanProduct(row pgx.Row) (api.ProductResponse, error) {
	var p api.ProductResponse
	err := row.Scan(&p.Id, &p.Name, &p.Description, &p.Price, &p.Stock, &p.Category, &p.Status, &p.SellerId, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func (s *Server) CreateProduct(ctx context.Context, req api.CreateProductRequestObject) (api.CreateProductResponseObject, error) {
	user := currentUser(ctx)
	if user.Role != api.RoleSELLER && user.Role != api.RoleADMIN {
		return nil, errAccessDenied()
	}

	b := req.Body
	product, err := scanProduct(s.db.QueryRow(ctx,
		`INSERT INTO products (name, description, price, stock, category, status, seller_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING `+productColumns,
		b.Name, b.Description, b.Price, b.Stock, b.Category, b.Status, user.ID,
	))
	if err != nil {
		return nil, err
	}
	return api.CreateProduct201JSONResponse(product), nil
}

func (s *Server) GetProduct(ctx context.Context, req api.GetProductRequestObject) (api.GetProductResponseObject, error) {
	product, err := scanProduct(s.db.QueryRow(ctx, `SELECT `+productColumns+` FROM products WHERE id = $1`, req.Id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errProductNotFound(req.Id)
	}
	if err != nil {
		return nil, err
	}
	return api.GetProduct200JSONResponse(product), nil
}

func (s *Server) ListProducts(ctx context.Context, req api.ListProductsRequestObject) (api.ListProductsResponseObject, error) {
	// Значения по умолчанию из спецификации (page=0, size=20).
	page, size := int32(0), int32(20)
	if req.Params.Page != nil {
		page = *req.Params.Page
	}
	if req.Params.Size != nil {
		size = *req.Params.Size
	}

	// Собираем WHERE только из переданных фильтров. Значения идут параметрами ($1, $2) — без SQL-инъекций.
	where := ` WHERE 1 = 1`
	var args []any
	if req.Params.Status != nil {
		args = append(args, *req.Params.Status)
		where += fmt.Sprintf(` AND status = $%d`, len(args))
	}
	if req.Params.Category != nil {
		args = append(args, *req.Params.Category)
		where += fmt.Sprintf(` AND category = $%d`, len(args))
	}

	result := api.ProductPage{Items: []api.ProductResponse{}, Page: page, Size: size}
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM products`+where, args...).Scan(&result.TotalElements); err != nil {
		return nil, err
	}

	args = append(args, size, int64(page)*int64(size))
	rows, err := s.db.Query(ctx,
		`SELECT `+productColumns+` FROM products`+where+
			fmt.Sprintf(` ORDER BY created_at DESC, id LIMIT $%d OFFSET $%d`, len(args)-1, len(args)),
		args...,
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

func (s *Server) UpdateProduct(ctx context.Context, req api.UpdateProductRequestObject) (api.UpdateProductResponseObject, error) {
	if err := s.checkProductAccess(ctx, req.Id); err != nil {
		return nil, err
	}

	b := req.Body
	product, err := scanProduct(s.db.QueryRow(ctx,
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

// DeleteProduct — мягкое удаление: запись остаётся, статус становится ARCHIVED.
func (s *Server) DeleteProduct(ctx context.Context, req api.DeleteProductRequestObject) (api.DeleteProductResponseObject, error) {
	if err := s.checkProductAccess(ctx, req.Id); err != nil {
		return nil, err
	}

	product, err := scanProduct(s.db.QueryRow(ctx,
		`UPDATE products SET status = 'ARCHIVED' WHERE id = $1 RETURNING `+productColumns, req.Id))
	if err != nil {
		return nil, err
	}
	return api.DeleteProduct200JSONResponse(product), nil
}

// checkProductAccess: ADMIN может менять любой товар, SELLER — только свой, USER — никакой.
func (s *Server) checkProductAccess(ctx context.Context, productID uuid.UUID) error {
	user := currentUser(ctx)
	if user.Role != api.RoleSELLER && user.Role != api.RoleADMIN {
		return errAccessDenied()
	}

	var sellerID uuid.UUID
	err := s.db.QueryRow(ctx, `SELECT seller_id FROM products WHERE id = $1`, productID).Scan(&sellerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errProductNotFound(productID)
	}
	if err != nil {
		return err
	}

	if user.Role == api.RoleSELLER && sellerID != user.ID {
		return errAccessDenied()
	}
	return nil
}
