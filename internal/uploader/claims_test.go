package uploader

import "testing"

func TestCanAccessBucket_ShouldGrantAccess_WhenExactMatch(t *testing.T) {
	c := &Claims{AllowedBuckets: []string{"foo-files"}}
	if !c.CanAccessBucket("foo-files") {
		t.Fatal("expected access to exact bucket")
	}
}

func TestCanAccessBucket_ShouldGrantAccess_WhenSubBucketOfAllowedParent(t *testing.T) {
	c := &Claims{AllowedBuckets: []string{"foo-files"}}
	if !c.CanAccessBucket("foo-files--level-1") {
		t.Fatal("expected access to sub-bucket of allowed parent")
	}
}

func TestCanAccessBucket_ShouldGrantAccess_WhenAdmin(t *testing.T) {
	c := &Claims{AllowedBuckets: []string{"*"}}
	if !c.CanAccessBucket("any-bucket") {
		t.Fatal("expected admin to access any bucket")
	}
}

func TestCanAccessBucket_ShouldDenyAccess_WhenNeitherMatchNorSubBucket(t *testing.T) {
	c := &Claims{AllowedBuckets: []string{"foo-files"}}
	if c.CanAccessBucket("bar-files") {
		t.Fatal("expected no access to unrelated bucket")
	}
}

func TestCanAccessBucket_ShouldDenyAccess_WhenPartialPrefixNotSubBucket(t *testing.T) {
	// "foo" should not grant access to "foobar" — must be "foo--bar"
	c := &Claims{AllowedBuckets: []string{"foo"}}
	if c.CanAccessBucket("foobar") {
		t.Fatal("expected no access: foobar is not a sub-bucket of foo")
	}
}

func TestOwnsBucket_ShouldReturnTrue_WhenExactMatch(t *testing.T) {
	c := &Claims{AllowedBuckets: []string{"foo-files"}, Role: "user"}
	if !c.OwnsBucket("foo-files") {
		t.Fatal("expected ownership of exact bucket")
	}
}

func TestOwnsBucket_ShouldReturnTrue_WhenSubBucketOfOwnedParent(t *testing.T) {
	c := &Claims{AllowedBuckets: []string{"foo-files"}, Role: "user"}
	if !c.OwnsBucket("foo-files--level-1") {
		t.Fatal("expected ownership of sub-bucket via parent")
	}
}

func TestOwnsBucket_ShouldReturnTrue_WhenAdmin(t *testing.T) {
	c := &Claims{Role: "admin"}
	if !c.OwnsBucket("any-bucket") {
		t.Fatal("expected admin to own any bucket")
	}
}

func TestOwnsBucket_ShouldReturnFalse_WhenWildcardOnly(t *testing.T) {
	// Wildcard non-admin should not count as ownership
	c := &Claims{AllowedBuckets: []string{"*"}, Role: "user"}
	if c.OwnsBucket("any-bucket") {
		t.Fatal("expected no ownership via wildcard for non-admin")
	}
}
