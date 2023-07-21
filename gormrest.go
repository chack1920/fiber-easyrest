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
	"errors"
	"fmt"
	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"reflect"
	"strconv"
	"strings"
)

// Options for the exposed GORM backed REST API.
// Delete, Mutate and Create are available to enable or disable mutation options.
// If all are false then the API is read only.
// A validation function is also optional.
// If the Validator returns falls 301 (unauthorized) is returned to ensure object presence is not leaked.
// Two Types are specified, T and D.  T is the storage type, and D is a DTO type.
// They can be the same.
// Fields from T are copied to identically named fields in D before being sent on the REST API as json.
// Inbound the reverse happens on any Mutate or Create.
type Options[T any, D any] struct {
	Delete    bool                                              // Enable delete
	Mutate    bool                                              // Enable mutate
	Create    bool                                              // Enable create
	Validator func(c *fiber.Ctx, action Action, item ...T) bool // Validation function, item is empty if this is a find all query or an item is not found
}

// DefaultOptions returns a basic configuration allowing all rest operations and with no authentication
func DefaultOptions[T any, D any]() Options[T, D] {
	return Options[T, D]{
		Delete: true,
		Mutate: true,
		Create: true,
		Validator: func(c *fiber.Ctx, action Action, item ...T) bool {
			return true
		},
	}
}

// Internal implementation
type grest[T any, D any] struct {
	Options[T, D]
	emptyT T // Empty template of T
	emptyD D // Empty template of D
	dMap   dtoMap
	db     *gorm.DB
}

// RegisterApi exposes an api underneath the app route using path and exposing objects of T.
// Objets of T are managed in db using GORM including mutations as enabled in Options.
// There must be a single string key field in the T option exposed as the tag `rest:"key"`.
// Child objects can be exposed either directly in the json by making them present in the Dto type or
// as sub-paths exposed as path/:id/field if specified using the tag `rest:"child"`.  If exposed as child paths
// the child objects are read only.  If exposed in the json then they will be part of the GORM mutation actions.
func RegisterApi[T any, D any](app fiber.Router, db *gorm.DB, path string, options Options[T, D]) {
	// Create the implementation
	impl := grest[T, D]{
		Options: options,
		db:      db,
	}

	// One off reflection of the types to create the field mappings.
	// They are stored in the impl.dMap.links as a tuple.  [0] is the dto field and [1] is the source field.
	// This reflection also finds the key and child tags.
	impl.dMap = buildDtoMap[T, D](impl.emptyT, impl.emptyD)

	// Create the grest struct, assuming all the features are exposed.
	fullApi := Api[T, D]{
		Path:        path,
		Find:        impl.finder,
		FindAll:     impl.findAll,
		FindAllPage: impl.findAllPage,
		Search:      impl.search,
		Mutate:      impl.mutate,
		Create:      impl.create,
		Delete:      impl.delete,
		SubEntities: []SubEntity[T, D]{},
		Validator:   impl.Validator,
		Dto:         impl.copyToDto,
	}
	// Remove any disabled options
	if !options.Delete {
		fullApi.Delete = nil
	}
	if !options.Mutate {
		fullApi.Mutate = nil
	}
	if !options.Create {
		fullApi.Create = nil
	}

	// Create the API child maps
	for _, c := range impl.dMap.children {
		name := impl.dMap.tT.Field(c).Name
		fullApi.SubEntities = append(fullApi.SubEntities, SubEntity[T, D]{
			SubPath: strings.ToLower(name),
			Get:     impl.children(c),
		})
	}

	// Finally register the API with Fiber
	RegisterAPI(app, fullApi)
}

// finder for single items.
// Makes used of the gorm Find() function passing in a template object that has just the key set.
func (a *grest[T, D]) finder(key string) (T, bool) {
	// Create the template item
	item, err := a.emptyWithKey(key)
	if err != nil {
		return item, false
	}
	// Find it.
	// Preload joined tables so that the object is fully populated.
	tx := a.db.Preload(clause.Associations).Limit(1).Find(&item, &item)

	// Return the result or error
	err2 := tx.Error
	cnt := tx.RowsAffected
	if err2 != nil || cnt != 1 {
		return a.emptyT, false
	}
	return item, true
}

// emptyWithKey creates an empty template of T filling in only the key field.
func (a *grest[T, D]) emptyWithKey(key string) (T, error) {
	// Start with our fully empty T
	item := a.emptyT

	// Get a mutable reflect.Value
	valObj := reflect.Indirect(reflect.ValueOf(&item))
	// And set our key field, selecting the appropriate type
	valDest := valObj.FieldByIndex(a.dMap.objKey)
	if valDest.CanSet() {
		switch {
		case valDest.CanInt():
			k, err := strconv.Atoi(key)
			if err != nil {
				return a.emptyT, errors.New("key value " + key + " is not an int")
			}
			valDest.SetInt(int64(k))
		case valDest.CanUint():
			k, err := strconv.Atoi(key)
			if err != nil {
				return a.emptyT, errors.New("key value " + key + " is not a uint")
			}
			valDest.SetUint(uint64(k))
		default:
			valDest.SetString(key)
		}
	} else {
		panic(fmt.Sprintf("key field '%s' is not settable", a.dMap.tT.FieldByIndex(a.dMap.objKey).Name))
	}
	return item, nil
}

// 标准分页结构体，接收最原始的DO
// 建议在外部再建一个字段一样的结构体，用以将DO转换成DTO或VO
type Page[T any] struct {
	CurrentPage int64 `json:"currentPage"`
	PageSize    int64 `json:"pageSize"`
	Total       int64 `json:"total"`
	Pages       int64 `json:"pages"`
	Data        []T   `json:"data"`
}

// 各种查询条件先在query设置好后再放进来
func (a *Page[T]) SelectPages(query *gorm.DB) (e error) {
	var model T
	query.Model(&model).Count(&a.Total)
	if a.Total == 0 {
		a.Data = []T{}
		return
	}
	e = query.Model(&model).Scopes(Paginate(a)).Find(&a.Data).Error
	return
}

func Paginate[T any](a *Page[T]) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if a.CurrentPage <= 0 {
			a.CurrentPage = 1
		}
		switch {
		case a.PageSize > 10000:
			a.PageSize = 10000 // 限制一下分页大小
		case a.PageSize <= 0:
			a.PageSize = 10
		}
		a.Pages = a.Total / a.PageSize
		if a.Total%a.PageSize != 0 {
			a.Pages++
		}
		p := a.CurrentPage
		if a.CurrentPage > a.Pages {
			p = a.Pages
		}
		size := a.PageSize
		offset := int((p - 1) * size)
		return db.Offset(offset).Limit(int(size))
	}
}

// findAll returns all the objects of T as a slice
func (a *grest[T, D]) findAllPage(ID int64) Page[T] {
	var all []T
	rest := a.db.Order("ID desc").Preload(clause.Associations).Find(&all)
	p := Page[T]{CurrentPage: ID, PageSize: 10}
	err := p.SelectPages(rest)
	if err != nil {
		return Page[T]{}
	}

	return p
}

func (a *grest[T, D]) findAll() []T {
	var all []T
	a.db.Order("ID desc").Preload(clause.Associations).Find(&all)
	//fmt.Println(rest.RowsAffected)

	return all
}

// search uses the D as a filter, providing it as a mask to the gorm find function
func (a *grest[T, D]) search(filter D) []T {
	tFilter := a.copyFromDto(a.emptyT, filter)
	var all []T
	a.db.Preload(clause.Associations).Find(&all, &tFilter)
	return all
}

// mutate takes a Dto of type D and applies it to an existing object of T.
// T is then persisted in the DB.
func (a *grest[T, D]) mutate(orig T, edit D) (T, error) {
	// Copy the dto
	orig = a.copyFromDto(orig, edit)
	// Save it to the database
	err := a.db.Save(&orig).Error
	return orig, err
}

// create inserts a new T built from a template T and D mutation + key field
func (a *grest[T, D]) create(edit D) (T, error) {
	// Create the new empty object with a key set
	key := reflect.ValueOf(edit).FieldByIndex(a.dMap.dtoKey)
	keyString := ""
	switch {
	case key.CanInt():
		keyString = strconv.Itoa(int(key.Int()))
	case key.CanUint():
		keyString = strconv.Itoa(int(key.Uint()))
	default:
		keyString = key.String()
	}
	if keyString == "" {
		return a.emptyT, errors.New("missing key value")
	}
	ret, err := a.emptyWithKey(keyString)
	if err != nil {
		return ret, err
	}
	// Copy the data and save
	return a.mutate(ret, edit)
}

// copyToDto does the heavy lifting of "cloning" T into its Dto D.
// This is done using the previously generated to avoid reflective lookups.
func (a *grest[T, D]) copyToDto(in T) (out D) {
	// If Dto and base are the same ... just return the data
	if a.dMap.tT == a.dMap.dT {
		val := reflect.ValueOf(in)
		return val.Interface().(D)
	}

	// Create a mutable reference to our Dto
	valObj := reflect.Indirect(reflect.ValueOf(&out))

	// For each field, set the Dto value
	for _, pair := range a.dMap.links {
		// Get our source
		from := reflect.ValueOf(in).FieldByIndex(pair.tField)

		// Get our destination
		valDest := valObj.FieldByIndex(pair.dField)
		if valDest.CanSet() {
			valDest.Set(from)
		} else {
			panic(fmt.Sprintf("immutable field '%s' found in dto transformation", a.dMap.dT.FieldByIndex(pair.dField).Name))
		}
	}
	return out
}

// copyFromDto does the heavy lifting for mutation by copying fields from the Dto back into the source for persisting.
// This is done using the previously generated to avoid reflective lookups.
func (a *grest[T, D]) copyFromDto(out T, in D) T {
	// Inbound there is no shortcut for identical types because of potentially missing json fields
	// We still need to copy the fields

	// Create a mutable reference to our source
	valObj := reflect.Indirect(reflect.ValueOf(&out))
	valIn := reflect.ValueOf(in)

	// Copy key field
	oKey := valObj.FieldByIndex(a.dMap.objKey)
	dKey := valIn.FieldByIndex(a.dMap.dtoKey)
	oKey.Set(dKey)

	// For each Dto field copy its value
	for _, pair := range a.dMap.links {
		// Get our destination field
		valDest := valObj.FieldByIndex(pair.tField)

		// And our source value
		from := valIn.FieldByIndex(pair.dField)
		if valDest.CanSet() {
			valDest.Set(from)
		} else {
			panic(fmt.Sprintf("immutable field '%s' applying dto to source", a.dMap.tT.FieldByIndex(pair.tField).Name))
		}
	}
	return out
}

// delete simply using GORM to delete the specified item.
// If gorm.Model is used then the object is not deleted, it is just marked as inactive in the database.
func (a *grest[T, D]) delete(item T) (T, error) {
	err := a.db.Delete(&item).Error
	return item, err
}

// children supplies a function implementation to source and return a specific child field
// identified as `rest:"child"`.  If the field is not a slice or array a panic will be triggered.
func (a *grest[T, D]) children(c int) func(item T) []any {
	return func(item T) []any {
		// Create return array
		var res []any
		// Get our child field
		children := reflect.ValueOf(item).Field(c)
		// Copy child values into the array - this will panic if children is not an Array or Slice
		for i := 0; i < children.Len(); i++ {
			res = append(res, children.Index(i).Interface())
		}
		return res
	}
}

type fieldLink struct {
	dField []int
	tField []int
}

type dtoMap struct {
	links    []fieldLink // 0 = dto, 1 = obj
	objKey   []int
	dtoKey   []int
	children []int
	dT       reflect.Type
	tT       reflect.Type
}

// Builds a mapping between the source and dto types.
// Mapping is produced for all Exported fields in the D type except those
// set to be ignored in the JSON (i.e. json="-").   This allows the same
// type to be used for both the source and the DTO without missing JSON types
// inadvertently overwriting source fields in the copy back.
func buildDtoMap[T any, D any](emptyT T, emptyD D) (dMap dtoMap) {
	tT := reflect.TypeOf(emptyT)
	dT := reflect.TypeOf(emptyD)
	modelT := reflect.TypeOf(gorm.Model{}) // We ignore the gorm.Model fields explicitly

	// One link for each field
	// find the matching field in the base struct for each field in the dto struct
	for i := 0; i < dT.NumField(); i++ {
		dF := dT.Field(i)
		jsonTags := dF.Tag.Get("json") // Ignore fields not in JSON
		if dF.IsExported() && jsonTags != "-" && dF.Type != modelT {
			tF, ok := tT.FieldByName(dF.Name)
			if !ok {
				panic(fmt.Sprintf("Missing dto field %s on base type %s", dF.Name, tT.Name()))
			}
			if tF.Type != dF.Type {
				panic(fmt.Sprintf("Mismatched types on %s.%s and %s.%s", dT.Name(), dF.Name, tT.Name(), tF.Name))
			}
			tIndex := tF.Index
			dIndex := dF.Index
			if tF.Name == dF.Name {
				dMap.links = append(dMap.links, fieldLink{dField: dIndex, tField: tIndex})
			}
		}
	}

	keyFound := false
	// Inspect all the base struct fields for tags
	for i := 0; i < tT.NumField(); i++ {
		tF := tT.Field(i)
		if tF.IsExported() {
			tags := tF.Tag.Get("rest")
			// Identify the key field
			if strings.Contains(tags, "key") {
				dMap.objKey = tF.Index
				keyFound = true
				keyField, ok := dT.FieldByName(tF.Name)
				if ok {
					dMap.dtoKey = keyField.Index
				} else {
					panic("Key field " + tF.Name + " missing on Dto type " + dT.Name())
				}
			}
			// Children to expose
			if strings.Contains(tags, "child") {
				dMap.children = append(dMap.children, i)
			}
		}
	}

	if !keyFound {
		// If no explicit key is set, try for an ID field like gorm
		idTF, ok := tT.FieldByName("ID")
		if !ok {
			panic("No key field found and no ID field for " + tT.Name())
		}
		idDF, ok := dT.FieldByName("ID")
		if !ok {
			panic("No key field ID found on " + dT.Name())
		}
		dMap.objKey = idTF.Index
		dMap.dtoKey = idDF.Index
	}

	dMap.dT = dT
	dMap.tT = tT

	return dMap
}
