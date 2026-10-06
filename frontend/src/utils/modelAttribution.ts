export function validAttributionExpectedModels(models?: string[] | null): boolean {
  return models == null || models.length <= 500 && models.every(model => {
    const id = model.trim()
    return id.length > 0 && id.length <= 200 && !/[\s*]/.test(id)
  })
}
