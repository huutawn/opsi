# Opsi Test Architecture & Navigation Guide

Tài liệu hướng dẫn cấu trúc phân tầng kiểm thử, quyền sở hữu fixture, quy tắc đặt file và lệnh thực thi chuẩn hóa trong kho mã nguồn Opsi.

---

## 1. Kiến trúc phân tầng kiểm thử (Hybrid Architecture)

Hệ thống kiểm thử Opsi áp dụng mô hình **hybrid**:
- **Go Tests**: Giữ file `*_test.go` nằm cạnh mã nguồn trong từng package để tối ưu hoá white-box testing và bảo toàn test coverage nội bộ.
- **Shared Fixtures & Assets**: Tập trung tại thư mục gốc `test/fixtures/` phục vụ các kịch bản kiểm thử tích hợp liên module hoặc harness container.
- **UI Tests**: Đặt tập trung tại `cli/ui/test/` (`unit/`, `e2e/`, `support/`).
- **Release / Remote Harnesses**: Đặt tại `scripts/e2e/` để đảm bảo tính ổn định của đường dẫn thực thi theo Git revision.

---

## 2. Bảng điều hướng các tầng kiểm thử (Test Tier Taxonomy)

| Tầng | Định danh | Vị trí file | Build Tag / Loại | Prerequisite | Lệnh Makefile |
|---|---|---|---|---|---|
| **1. Go Unit** | Unit Tests | `agent/`, `cli/`, `cloud/`, `contracts/go/`, `test/` | Không tag (`_test.go`) | Go 1.26.x | `make test` hoặc `make test-go-unit` |
| **2. UI Unit** | Node Unit Tests | `cli/ui/test/unit/` (`app/`, `features/`, `lib/`, `support/`) | Node.js test runner (`*.test.mjs`) | Node.js 24.x, npm | `make test-ui-unit` (`npm test --prefix cli/ui`) |
| **3. UI E2E** | Playwright Specs | `cli/ui/test/e2e/` | Playwright (`*.spec.ts`) | Chromium browser | `make test-ui-e2e` (`npx --prefix cli/ui playwright test`) |
| **4. PostgreSQL Integration** | Database Integration | `cloud/internal/...` | `//go:build postgresintegration` | Docker (chạy `postgres:16`) hoặc `OPSI_TEST_DATABASE_URL` | `make test-postgres-integration` (hoặc `make verify-postgres`) |
| **5. BuildKit Integration** | Container Buildpacks | `cloud/internal/buildexecutor/` | `//go:build buildkitintegration` | Docker / BuildKit daemon | `make test-buildpacks-integration` (hoặc `make verify-buildpacks-e2e`) |
| **6. K3s / Acceptance** | Real Cluster Acceptance | `agent/internal/svcatalog/`, `scripts/e2e/` | K3s cluster environment | Cụm K3s (container hoặc host), OCI Registry | `make test-e2e-k3s`, `make test-e2e-private-registry`, `make test-e2e-node-lifecycle` |
| **7. Live Smoke** | Deployment & Barrier | `scripts/e2e/`, `deploy/` | Script vận hành | Live runtime, staging credentials | `make smoke-release`, `make test-e2e-dev-control-plane` |

---

## 3. Quyền sở hữu và cấu trúc Fixtures (`test/fixtures/`)

Thư mục gốc `test/` là authority duy nhất cho các test assets và fixtures dùng chung (không chứa Go unit tests của ứng dụng chính):

- **`test/fixtures/agent/`**:
  - `p07b2-application/`: Fixture ứng dụng Go kiểm thử xác thực và kết nối Valkey/Redis cache binding.
- **`test/fixtures/cloud/`**:
  - `adc02-consumer/`: Fixture HTTP service xác minh kết nối cơ sở dữ liệu và cache runtime.
  - `adc06-web/`: Fixture frontend tĩnh và reverse-proxy HTTP kiểm thử ingress/exposure closure.
  - `p07b3b1-application/`: Fixture ứng dụng Go kiểm thử vòng đời kết nối PostgreSQL runtime và state persistence.

### Nguyên tắc quản lý Fixtures:
1. **Không lưu giữ fixture chết**: Mọi fixture phải có caller hoặc kịch bản kiểm thử tham chiếu trực tiếp. Các fixture thử nghiệm không còn caller (như `sample-service`) phải bị loại bỏ hoàn toàn.
2. **Độc lập và gọn nhẹ**: Các fixture container biên dịch nhanh bằng `-trimpath` và chạy dưới dạng scratch container hoặc binary độc lập.

---

## 4. Cấu trúc UI Test (`cli/ui/test/`)

Toàn bộ kiểm thử của giao diện UI được tổ chức độc quyền trong `cli/ui/test/`:

```
cli/ui/test/
├── e2e/                             # Playwright specs
│   ├── assistant.spec.ts
│   ├── deploy.spec.ts
│   ├── i18n.spec.ts
│   ├── observability.spec.ts
│   └── security.spec.ts
├── support/                         # Trợ thủ dùng chung cho E2E
│   └── console-errors.ts
└── unit/                            # Node unit tests (cây con theo miền chức năng)
    ├── app/
    │   └── design-system.test.mjs
    ├── features/
    │   ├── auth/
    │   │   └── project-selection.test.mjs
    │   ├── console/
    │   │   └── navigation.test.mjs
    │   ├── deploy/
    │   │   ├── connection-descriptors.test.mjs
    │   │   ├── deploy.test.mjs
    │   │   └── runtime-config.test.mjs
    │   ├── observability/
    │   │   └── observability.test.mjs
    │   ├── security/
    │   │   └── security-view.test.mjs
    │   └── settings/
    │       └── settings-view.test.mjs
    ├── lib/
    │   ├── i18n/
    │   │   └── i18n.test.mjs
    │   └── presentation/
    │       ├── build.test.mjs
    │       ├── project.test.mjs
    │       ├── infrastructure/
    │       │   └── model.test.mjs
    │       ├── observability/
    │       │   └── model.test.mjs
    │       └── security/
    │           └── model.test.mjs
    └── support/
        └── console-errors.test.mjs
```

---

## 5. Quy tắc đặt file và đóng góp test mới

1. **Go Unit Test**:
   - Đặt file `*_test.go` cùng thư mục với package được kiểm thử.
   - Sử dụng package name cùng tên hoặc `package <name>_test` cho black-box testing.
   - File dữ liệu thử nghiệm tĩnh cục bộ đặt trong thư mục `testdata/` cạnh package.
2. **Go Integration Test**:
   - Thêm build tag rõ ràng ở dòng đầu tiên: `//go:build <tag>`.
   - Giữ nguyên vị trí cạnh package nguồn liên quan. Không tạo package `utils/` hoặc `common/` dùng chung khi chưa có ít nhất 2 ca sử dụng thực tế độc lập.
3. **UI Test**:
   - Unit test đặt trong `cli/ui/test/unit/<domain>/<name>.test.mjs`.
   - E2E spec đặt trong `cli/ui/test/e2e/<feature>.spec.ts`.
   - Không đặt file test trong các thư mục mã nguồn UI (`features/`, `lib/`, `app/`).
4. **Makefile Targets**:
   - Luôn ủy quyền trực tiếp cho lệnh hoặc target authority hiện hữu. Không nhân bản logic shell script hoặc lệnh docker run giữa nhiều target.
