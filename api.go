// MIT License
//
// Copyright (c) 2023 Seth Osher
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package easyrest

import (
	"github.com/gofiber/fiber/v2"
	"log"
	"strconv"
)

type SubEntity[T any, D any] struct {
	SubPath string
	Get     func(item T) []any
}

// Api is the easy rest/crud API for Fiber.
// Supply functions to find and mutate data objects and the Api will handle the rest implementation.
// The Api is defined by two generic types.
// The first, T, is the underlying dats type.
// The second, D, is a data transport type (DTO) used for the JSON in the API.
// The two types can be the same, but separating them gives additional flexibility to have different json transformations
// for internal and external API uses.
// See examples.
type Api[T any, D any] struct {
	Path        string                     // The path of the api under the parent
	Find        func(key string) (T, bool) // Find one method
	FindAllPage func(ID int64) Page[T]     //paginator.Page[T]   // Find all method
	FindAll     func() []T
	Search      func(D) []T                                       // Search using D as a filter
	Mutate      func(T, D) (T, error)                             // Mutation function for "PUT".  If nil, no mutation is exposed
	Create      func(D) (T, error)                                // Create function for "PUT".  If nil, creation is not exposed
	Delete      func(T) (T, error)                                // // Mutation function for "DELETE", if nil, no mutation is exposed
	SubEntities []SubEntity[T, D]                                 // SubEntities to expose as read only lists
	Dto         func(T) D                                         // Fill a DTO for T
	Validator   func(c *fiber.Ctx, action Action, item ...T) bool // Access check, T will be missing for aggregate functions or if the item is not found
}

type Action uint8

const (
	ActionGetAll Action = iota
	ActionGetOne
	ActionMutate
	ActionCreate
	ActionDelete
)

func RegisterAPI[T any, D any](api fiber.Router, genericApi Api[T, D]) {
	log.Printf("Registering REST api %s\n", genericApi.Path)

	// The api path
	generic := api.Group("/" + genericApi.Path)

	// The two variants of GetAll
	generic.Get("/", getAll[T, D](genericApi))
	generic.Get("/page/:id", getAllPage[T, D](genericApi))
	// The POST create  (if provided)
	if genericApi.Mutate != nil {
		generic.Post("/", createOne[T, D](genericApi))

	}

	// The POST search  (if provided)
	if genericApi.Search != nil {
		generic.Post("/filter", search[T, D](genericApi))

	}

	// The SubEntity getters
	// This is before the item Getter to ensure any name collision resolves to the SubEntity
	for _, subEntity := range genericApi.SubEntities {
		generic.Get("/:id/"+subEntity.SubPath, getSubEntity[T, D](genericApi, subEntity.Get))
	}

	// The Single item Getter
	generic.Get("/:id", getOne[T, D](genericApi))

	// The PUT mutation (if provided)
	if genericApi.Mutate != nil {
		generic.Put("/:id", mutateOne[T, D](genericApi))

	}

	// The GET mutation (if provided)
	if genericApi.Delete != nil {
		generic.Delete("/:id", deleteOne[T, D](genericApi))

	}
}

// getAll returns all entities as their Jdo type
func getAll[T any, D any](api Api[T, D]) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Perms check
		if api.Validator != nil && !api.Validator(c, ActionGetAll) {
			return c.SendStatus(fiber.StatusUnauthorized)
		}

		// Find all
		// Transform to DTO
		// Send as JSON
		var all []D

		for _, v := range api.FindAll() {
			all = append(all, api.Dto(v))

		}
		return c.JSON(all)
	}
}
func getAllPage[T any, D any](api Api[T, D]) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Perms check
		id := c.Params("id")
		i, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			// 可能字符串 s 不是合法的整数格式，处理错误
		}

		if api.Validator != nil && !api.Validator(c, ActionGetAll) {
			return c.SendStatus(fiber.StatusUnauthorized)
		}
		// Find all
		// Transform to DTO
		// Send as JSON
		var all Page[T]

		all = api.FindAllPage(i)
		return c.JSON(all)

	}
}

// getAll returns all entities as their Jdo type
func search[T any, D any](api Api[T, D]) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Perms check
		if api.Validator != nil && !api.Validator(c, ActionGetAll) {
			return c.SendStatus(fiber.StatusUnauthorized)
		}

		var filter D
		if err := c.BodyParser(&filter); err != nil {
			log.Printf("Error parsing body %v\n", err)
			return c.SendStatus(fiber.StatusBadRequest)
		}

		// Search with filter
		// Transform to DTO
		// Send as JSON
		var all []D
		for _, v := range api.Search(filter) {
			all = append(all, api.Dto(v))
		}
		return c.JSON(all)
	}
}

// getOne returns a single Jdo for a single item on the path.
// 404 if entity is not in the cache
func getOne[T any, D any](api Api[T, D]) fiber.Handler {
	return func(c *fiber.Ctx) error {

		// Find the item
		id := c.Params("id")
		item, ok := api.Find(id)
		if !ok {
			// don't leak existence information if unauthorized
			if api.Validator != nil && !api.Validator(c, ActionGetOne) {
				return c.SendStatus(fiber.StatusUnauthorized)
			}
			return c.SendStatus(fiber.StatusNotFound)
		}

		// Perms check
		if api.Validator != nil && !api.Validator(c, ActionGetOne, item) {
			return c.SendStatus(fiber.StatusUnauthorized)
		}

		// Return DTO JSON
		return c.JSON(api.Dto(item))
	}
}

func createOne[T any, D any](api Api[T, D]) fiber.Handler {
	return func(c *fiber.Ctx) error {

		// We don't need to check if creation is enabled because the POST function won't be registered

		var amended D
		if err := c.BodyParser(&amended); err != nil {
			log.Printf("Error parsing body %v\n", err)
			return c.SendStatus(fiber.StatusBadRequest)
		}

		if api.Validator != nil && !api.Validator(c, ActionCreate) {
			return c.SendStatus(fiber.StatusUnauthorized)
		}

		// Create
		item, err := api.Create(amended)
		if err != nil {
			log.Printf("Error creating item: %v, %v\n", item, err)
			return c.SendStatus(fiber.StatusInternalServerError)
		}
		return c.JSON(api.Dto(item))
	}
}

// mutateOne returns a single Jdo for a single item on the path after mutation from the supplied Jdo JSON in the body
// 404 if entity is not in the cache
// 400 if the body cannot be parsed or the mime type is not json
func mutateOne[T any, D any](api Api[T, D]) fiber.Handler {
	return func(c *fiber.Ctx) error {

		// Parse the body
		var amended D
		if err := c.BodyParser(&amended); err != nil {
			log.Printf("Error parsing body %v\n", err)
			return c.SendStatus(fiber.StatusBadRequest)
		}

		// Find the item
		id := c.Params("id")
		item, ok := api.Find(id)
		var err error
		if !ok {
			// Perms check for creation
			if api.Validator != nil && !api.Validator(c, ActionMutate) {
				return c.SendStatus(fiber.StatusUnauthorized)
			}
			// If not found
			return c.SendStatus(fiber.StatusNotFound)
		} else {
			// Perms check
			if api.Validator != nil && !api.Validator(c, ActionMutate, item) {
				return c.SendStatus(fiber.StatusUnauthorized)
			}
			item, err = api.Mutate(item, amended)
			if err != nil {
				log.Printf("Error mutating item: %v, %v\n", item, err)
				return c.SendStatus(fiber.StatusInternalServerError)
			}
		}

		return c.JSON(api.Dto(item))
	}
}

// deleteOne returns a single Jdo for a single item on the path after mutation/deletion
// 404 if entity is not in the cache
func deleteOne[T any, D any](api Api[T, D]) fiber.Handler {
	return func(c *fiber.Ctx) error {

		id := c.Params("id")
		item, ok := api.Find(id)
		if !ok {
			// don't leak existence information if unauthorized
			if api.Validator != nil && !api.Validator(c, ActionDelete) {
				return c.SendStatus(fiber.StatusUnauthorized)
			}
			return c.SendStatus(fiber.StatusNotFound)
		}

		if api.Validator != nil && !api.Validator(c, ActionDelete, item) {
			return c.SendStatus(fiber.StatusUnauthorized)
		}

		var err error
		item, err = api.Delete(item)
		if err != nil {
			log.Printf("Error deleting item: %v\n", err)
			return c.SendStatus(fiber.StatusInternalServerError)
		}

		return c.SendString("deleted")
	}
}

// getSubEntity fulfils a request for a SubEntity of the request item :id, supplied by the getter function
// 404 if entity is not in the cache
func getSubEntity[T any, D any](api Api[T, D], getter func(entity T) []any) fiber.Handler {
	return func(c *fiber.Ctx) error {

		id := c.Params("id")
		item, ok := api.Find(id)
		if !ok {
			// don't leak existence information if unauthorized
			if api.Validator != nil && !api.Validator(c, ActionGetOne) {
				return c.SendStatus(fiber.StatusUnauthorized)
			}
			return c.SendStatus(fiber.StatusNotFound)
		}

		if api.Validator != nil && !api.Validator(c, ActionGetOne, item) {
			return c.SendStatus(fiber.StatusUnauthorized)
		}

		subAll := getter(item)
		return c.JSON(subAll)
	}

}
