IMAGE_REPO ?= docker.io/lentil1016
IMAGE_TAG  ?= latest

.PHONY: build-mcp build-backend build-frontend docker-build docker-save deploy test clean

build-mcp:
	cd mcp-server && CGO_ENABLED=0 go build -o mcp-server .

build-backend:
	cd backend && CGO_ENABLED=0 go build -o backend .

build-frontend:
	cd frontend && npm ci && npm run build

docker-build:
	docker build -t $(IMAGE_REPO)/kubeassist-mcp:$(IMAGE_TAG) mcp-server/
	docker build -t $(IMAGE_REPO)/kubeassist-backend:$(IMAGE_TAG) backend/
	docker build -t $(IMAGE_REPO)/kubeassist-frontend:$(IMAGE_TAG) frontend/

docker-save:
	docker save \
		$(IMAGE_REPO)/kubeassist-mcp:$(IMAGE_TAG) \
		$(IMAGE_REPO)/kubeassist-backend:$(IMAGE_TAG) \
		$(IMAGE_REPO)/kubeassist-frontend:$(IMAGE_TAG) \
		-o kubeassist-images.tar

deploy:
	kubectl apply -k deploy/base/

test:
	cd mcp-server && go test ./...
	cd backend && go test ./...
	cd frontend && npm test

clean:
	rm -f mcp-server/mcp-server backend/backend
	rm -rf frontend/dist frontend/node_modules
	rm -f kubeassist-images.tar
